package storage

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/mimalef70/goomni/src/domains"
)

const providerMediaSchema = `CREATE TABLE provider_media(connection_id TEXT NOT NULL REFERENCES devices(connection_id),peer_key TEXT NOT NULL,message_id TEXT NOT NULL,cipher BLOB,event_time INTEGER NOT NULL,PRIMARY KEY(connection_id,peer_key,message_id))`

type privateProviderMedia struct {
	FileID      string          `json:"file_id"`
	AccessHash  string          `json:"access_hash"`
	Size        int64           `json:"size"`
	Name        string          `json:"name"`
	ContentType string          `json:"content_type"`
	Data        json.RawMessage `json:"data,omitempty"`
}

// The durable event and delivery body must agree with the private reference
// accepted in their transaction. A quote's attachment belongs to another
// message, so only the current content chain may advertise this registration.
func (s *Store) setEventMediaAvailability(event *domains.Event, available bool) error {
	fileID := ""
	if event.Media != nil {
		fileID = event.Media.FileID
	}
	if event.Message != nil && event.Message.Media != nil {
		message, media := *event.Message, *event.Message.Media
		media.DownloadSupported = available && fileID != "" && media.FileID == fileID
		message.Media = &media
		event.Message = &message
	}
	contract, err := s.mediaContract(event.Provider)
	if err != nil {
		return err
	}
	return contract.ProjectMediaAvailability(event, available)
}

func (s *Store) validateProviderMediaKey(provider domains.Provider, peer domains.Peer, id string) error {
	c, err := s.mediaContract(provider)
	if err != nil {
		return err
	}
	return c.ValidateMediaKey(peer, id)
}
func (s *Store) validateProviderMedia(provider domains.Provider, m domains.ProviderMedia) error {
	if m.Provider != provider {
		return providerMismatch()
	}
	if m.Version < 1 {
		return domains.E("INVALID_PROVIDER_MEDIA", "private media format version is required", 502)
	}
	if m.Size < 0 || len(m.Name) > 4096 || len(m.ContentType) > 255 {
		return domains.E("INVALID_PROVIDER_MEDIA", "invalid provider media metadata", 502)
	}
	c, err := s.mediaContract(provider)
	if err != nil {
		return err
	}
	return c.ValidateMediaReference(m)
}
func (s *Store) validateMediaRevision(provider domains.Provider, peer domains.Peer, kind string, revision *domains.MediaRevision) error {
	c, err := s.mediaContract(provider)
	if err != nil {
		return err
	}
	return c.ValidateMediaRevision(peer, kind, revision)
}
func providerMediaAAD(conn string, peer domains.Peer, messageID string) string {
	return conn + ":provider-media:" + peer.Key() + ":" + messageID
}
func (s *Store) saveProviderMediaTx(ctx context.Context, tx *sql.Tx, conn string, peer domains.Peer, messageID string, m *domains.ProviderMedia, eventTime int64, kind string, revision *domains.MediaRevision) (bool, error) {
	provider, e := providerTx(ctx, tx, conn)
	if e != nil {
		return false, e
	}
	if e := s.validateProviderMediaKey(provider, peer, messageID); e != nil {
		return false, e
	}
	if e := s.validateMediaRevision(provider, peer, kind, revision); e != nil {
		return false, e
	}
	var ciphertext []byte
	var privateJSON []byte
	if m != nil {
		if e := s.validateProviderMedia(provider, *m); e != nil {
			return false, e
		}
		private := privateProviderMedia{FileID: m.FileID, AccessHash: m.AccessHash, Size: m.Size, Name: m.Name, ContentType: m.ContentType, Data: m.Data}
		plain, e := json.Marshal(private)
		if e != nil {
			return false, e
		}
		privateJSON = plain
		ciphertext, e = s.encodePrivate(provider, m.Version, plain, providerMediaAAD(conn, peer, messageID))
		if e != nil {
			return false, e
		}
	}
	return s.writeProviderMediaTx(ctx, tx, conn, provider, peer, messageID, m, ciphertext, privateJSON, eventTime, kind, revision)
}

// SaveProviderMedia registers references returned by this connection's history.
// It must not be exposed as an API accepting caller-supplied hashes. History
// without an update timestamp only initializes absent references (event_time=0),
// so later history reads cannot overwrite edits or deletion tombstones. The bool
// reports whether this exact reference is now available, including an identical
// existing reference. Persistence errors never report availability.
func (s *Store) SaveProviderMedia(ctx context.Context, conn string, peer domains.Peer, messageID string, m domains.ProviderMedia) (bool, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	if e = activeTx(ctx, tx, conn); e != nil {
		return false, e
	}
	available, e := s.saveProviderMediaTx(ctx, tx, conn, peer, messageID, &m, 0, "history", nil)
	if e != nil {
		return false, e
	}
	if e = tx.Commit(); e != nil {
		return false, e
	}
	return available, nil
}
func (s *Store) GetProviderMedia(ctx context.Context, conn string, peer domains.Peer, messageID string) (domains.ProviderMedia, error) {
	d, e := s.DeviceByConnection(ctx, conn)
	if e != nil {
		return domains.ProviderMedia{}, e
	}
	if e := s.validateProviderMediaKey(d.Provider, peer, messageID); e != nil {
		return domains.ProviderMedia{}, e
	}
	var ciphertext []byte
	e = s.db.QueryRowContext(ctx, `SELECT m.cipher FROM provider_media m JOIN devices d ON d.connection_id=m.connection_id WHERE m.connection_id=? AND m.peer_key=? AND m.message_id=? AND d.deleted_at IS NULL`, conn, peer.Key(), messageID).Scan(&ciphertext)
	if e != nil {
		return domains.ProviderMedia{}, dbError(e)
	}
	if len(ciphertext) == 0 {
		return domains.ProviderMedia{}, notFound()
	}
	envelope, e := s.decodePrivate(ciphertext, providerMediaAAD(conn, peer, messageID), d.Provider)
	if e != nil {
		return domains.ProviderMedia{}, e
	}
	var m privateProviderMedia
	if e = json.Unmarshal(envelope.Payload, &m); e != nil {
		return domains.ProviderMedia{}, invalidPrivateData("media")
	}
	return domains.ProviderMedia{Provider: d.Provider, Version: envelope.Version, FileID: m.FileID, AccessHash: m.AccessHash, Size: m.Size, Name: m.Name, ContentType: m.ContentType, Data: m.Data}, nil
}
