package usecase

import (
	"context"
	"io"
	"strings"

	"github.com/mimalef70/goomni/src/domains"
)

// DownloadAvatar selects an immutable account client before resolving the
// contact image. The caller cannot supply a provider URL, file ID or hash.
func (s *MediaService) DownloadAvatar(ctx context.Context, id string, user domains.Peer, size string) (io.ReadCloser, domains.AvatarInfo, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, domains.AvatarInfo{}, err
	}
	if user.AccessHash != "" {
		return nil, domains.AvatarInfo{}, domains.E("INVALID_PEER", "caller-supplied access hashes are not accepted", 400)
	}
	contract, err := s.provider(d.Provider)
	if err != nil {
		return nil, domains.AvatarInfo{}, err
	}
	if user.Type != "user" {
		return nil, domains.AvatarInfo{}, domains.E("INVALID_PEER", "avatar requires a user peer", 400)
	}
	if err = contract.ValidatePeer(user); err != nil {
		return nil, domains.AvatarInfo{}, err
	}
	if size == "" {
		size = "small"
	}
	if size != "small" && size != "large" {
		return nil, domains.AvatarInfo{}, domains.E("INVALID_REQUEST", "avatar size must be small or large", 400)
	}
	e, err := s.entry(d)
	if err != nil {
		return nil, domains.AvatarInfo{}, err
	}
	e.clientMu.RLock()
	client := e.client
	e.clientMu.RUnlock()
	downloader, ok := client.(domains.AvatarDownloader)
	if !ok {
		return nil, domains.AvatarInfo{}, domains.Unsupported("contact avatar download")
	}
	lifetime, cancel := context.WithCancel(ctx)
	s.mu.RLock()
	serviceContext := s.ctx
	s.mu.RUnlock()
	stopService := func() bool { return false }
	if serviceContext != nil {
		stopService = context.AfterFunc(serviceContext, cancel)
	}
	release := func() { stopService(); cancel() }
	handedOff := false
	defer func() {
		if !handedOff {
			release()
		}
	}()
	reader, info, err := downloader.DownloadAvatar(lifetime, user, size)
	if err != nil {
		if reader != nil {
			_ = reader.Close()
		}
		return nil, domains.AvatarInfo{}, safeError(err)
	}
	if reader == nil {
		return nil, domains.AvatarInfo{}, domains.E("AVATAR_INVALID", "provider returned no avatar stream", 502)
	}
	handedOff = true
	return &cancelledReader{ReadCloser: reader, cancel: release}, info, nil
}

// Download accepts only an account-scoped message reference. Provider file IDs
// and access hashes are loaded from encrypted storage, never from the caller.
// The returned reader owns its child context until Close; the HTTP adapter must
// close it even if the consuming client disconnects early.
func (s *MediaService) Download(ctx context.Context, id string, peer domains.Peer, messageID string) (io.ReadCloser, domains.ProviderMedia, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, domains.ProviderMedia{}, err
	}
	contract, err := s.provider(d.Provider)
	if err != nil {
		return nil, domains.ProviderMedia{}, err
	}
	validID := domains.ValidOpaqueID(messageID)
	if identity, ok := contract.(domains.MessageIdentityContract); ok {
		validID = validID && identity.ValidateMessageID(messageID)
	}
	if !validID {
		return nil, domains.ProviderMedia{}, domains.E("INVALID_MESSAGE_ID", "message_id is invalid for this provider", 400)
	}
	if err = contract.ValidatePeer(peer); err != nil {
		return nil, domains.ProviderMedia{}, err
	}
	descriptor, err := s.mediaStore.GetProviderMedia(ctx, d.ConnectionID, peer, messageID)
	if err != nil {
		return nil, domains.ProviderMedia{}, err
	}
	e, err := s.entry(d)
	if err != nil {
		return nil, domains.ProviderMedia{}, err
	}
	// Capturing the provider under a short pointer lock avoids holding the
	// account's lifecycle/send lock during file transfer. Its connection identity
	// can never be rebound; concurrent logout may abort this request, not reroute it.
	e.clientMu.RLock()
	client := e.client
	e.clientMu.RUnlock()
	downloader, ok := client.(domains.MediaDownloader)
	if !ok {
		return nil, domains.ProviderMedia{}, domains.Unsupported("provider media download")
	}
	lifetime, cancel := context.WithCancel(ctx)
	s.mu.RLock()
	serviceContext := s.ctx
	s.mu.RUnlock()
	stopService := func() bool { return false }
	if serviceContext != nil {
		stopService = context.AfterFunc(serviceContext, cancel)
	}
	release := func() { stopService(); cancel() }
	handedOff := false
	defer func() {
		if !handedOff {
			release()
		}
	}()
	reader, err := downloader.Download(lifetime, descriptor)
	if err != nil {
		if reader != nil {
			_ = reader.Close()
		}
		return nil, domains.ProviderMedia{}, safeError(err)
	}
	if reader == nil {
		return nil, domains.ProviderMedia{}, domains.E("INVALID_PROVIDER_MEDIA", "provider returned no media stream", 502)
	}
	handedOff = true
	return &cancelledReader{ReadCloser: reader, cancel: release}, descriptor, nil
}

// RegisterMedia commits metadata only for the immutable connection selected by
// the request. Staging ownership and file sync remain the media manager's job.
func (s *MediaService) RegisterMedia(ctx context.Context, id string, media domains.Media) error {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return err
	}
	if media.ConnectionID != d.ConnectionID {
		return domains.E("DEVICE_SCOPE_MISMATCH", "media belongs to another connection", 409)
	}
	return s.mediaStore.SaveMedia(ctx, media)
}

func (s *MediaService) LocalMedia(ctx context.Context, id, mediaID string) (domains.Media, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.Media{}, err
	}
	return s.mediaStore.GetMedia(ctx, d.ConnectionID, mediaID)
}

// Cleanup uses immutable owners, including deleted accounts. It must not apply
// the active-alias resolver or mistake a deleted account's media for an orphan.
func (s *MediaService) MediaFileRegistered(ctx context.Context, connection, id, relative, absolute string) (bool, error) {
	return s.mediaStore.MediaFileRegistered(ctx, connection, id, relative, absolute)
}

// SendUpload accepts a new owned local asset and enqueues its send atomically.
// Provider contact remains exclusively in the durable outbox worker.
func (s *MediaService) SendUpload(ctx context.Context, id string, request domains.SendRequest, key string, media domains.Media, digest string) (domains.Operation, error) {
	if request.Operation != "" || len(request.Payload) > 0 || request.RequestID != "" || request.MediaID != media.ID {
		return domains.Operation{}, domains.E("INVALID_REQUEST", "multipart sends require a new file and gateway-assigned request identity", 400)
	}
	switch request.Kind {
	case "file", "image", "audio", "video", "voice":
	default:
		return domains.Operation{}, domains.E("INVALID_REQUEST", "multipart sends require a media endpoint", 400)
	}
	if request.IsScheduled() {
		return domains.Operation{}, domains.E("USE_SCHEDULE_ENDPOINT", "upload media separately and use the schedule endpoint for delayed sends", 400)
	}
	if err := request.Validate(); err != nil {
		return domains.Operation{}, err
	}
	if strings.TrimSpace(key) == "" || len(key) > 256 {
		return domains.Operation{}, domains.E("INVALID_IDEMPOTENCY_KEY", "a nonempty idempotency key of at most 256 bytes is required", 400)
	}
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.Operation{}, err
	}
	if err = s.validateSend(d, request); err != nil {
		return domains.Operation{}, err
	}
	if d.ConnectionID != media.ConnectionID {
		return domains.Operation{}, domains.E("DEVICE_SCOPE_MISMATCH", "upload belongs to another connection", 409)
	}
	return s.sendStore.EnqueueUpload(ctx, d.ConnectionID, request, key, media, digest, s.admissionLimits())
}
