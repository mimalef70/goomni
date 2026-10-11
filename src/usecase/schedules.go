package usecase

import (
	"context"
	"strings"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/validations"
)

func (s *ScheduleService) ListSchedulesFiltered(ctx context.Context, id string, f domains.ScheduleFilter) ([]domains.Schedule, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	if err = s.validateQueryIdentity(d, f.Peer, ""); err != nil {
		return nil, err
	}
	if err = s.validateOperationFilter(d, f.Operation); err != nil {
		return nil, err
	}
	f.Limit, f.Offset = page(f.Limit, f.Offset)
	return s.scheduleStore.ListSchedulesFiltered(ctx, d.ConnectionID, f)
}

func (s *ScheduleService) ListScheduleOccurrences(ctx context.Context, id, scheduleID, state string, limit, offset int) ([]domains.ScheduleOccurrence, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	limit, offset = page(limit, offset)
	return s.scheduleStore.ListScheduleOccurrences(ctx, d.ConnectionID, scheduleID, state, limit, offset)
}

func (s *ScheduleService) CreateSchedule(ctx context.Context, id string, request domains.SendRequest) (domains.Schedule, error) {
	return s.CreateScheduleIdempotent(ctx, id, request, "")
}

func (s *ScheduleService) CreateScheduleIdempotent(ctx context.Context, id string, request domains.SendRequest, key string) (domains.Schedule, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.Schedule{}, err
	}
	if len(key) > 256 {
		return domains.Schedule{}, domains.E("INVALID_IDEMPOTENCY_KEY", "idempotency key exceeds 256 bytes", 400)
	}
	if request.Operation != "" || len(request.Payload) > 0 || request.Kind == "operation" {
		// Only reviewed message producers may recur. Administrative operations,
		// stories and financial actions are never admitted by this scheduler.
		contract, err := s.provider(d.Provider)
		if err != nil {
			return domains.Schedule{}, err
		}
		definition, exists := operationDefinition(contract, request.Operation)
		if !exists {
			return domains.Schedule{}, domains.Unsupported(request.Operation)
		}
		if !definition.Schedulable || definition.Mode != "mutation" {
			return domains.Schedule{}, domains.E("INVALID_REQUEST", "this operation cannot be scheduled", 400)
		}
		if (request.Kind != "" && request.Kind != "operation") || request.Phone != "" || request.Text != "" || request.ReplyMessageID != "" || request.MediaID != "" || len(request.Mentions) > 0 {
			return domains.Schedule{}, domains.E("INVALID_REQUEST", "scheduled operations take message fields inside payload", 400)
		}
		normalized, peer, err := s.normalizeMutation(d, request.Operation, request.Payload)
		if err != nil {
			return domains.Schedule{}, err
		}
		if (request.Peer.ID != "" || request.Peer.Type != "") && request.Peer != peer {
			return domains.Schedule{}, domains.E("INVALID_REQUEST", "conflicting scheduled operation destinations", 400)
		}
		request.Kind, request.Peer, request.Payload = "operation", peer, normalized
	} else if err := s.validateSend(d, request); err != nil {
		return domains.Schedule{}, err
	}
	if !request.IsScheduled() {
		return domains.Schedule{}, domains.E("INVALID_SCHEDULE", "scheduled_at is required", 400)
	}
	if request.RequestID != "" {
		return domains.Schedule{}, domains.E("INVALID_REQUEST", "request_id is assigned by the gateway", 400)
	}
	if request.Kind == "" {
		request.Kind = "text"
	}
	if key != "" {
		// A retry can arrive after scheduled_at or completion. Existing matching
		// work wins before new-schedule time and media availability checks.
		previous, found, err := s.scheduleStore.LookupScheduleIdempotent(ctx, d.ConnectionID, request, key)
		if err != nil || found {
			return previous, err
		}
	}
	// Historical accepted requests keep their original hash and calendar data.
	// New requests use the single current public spelling for one-time sends.
	if strings.EqualFold(strings.TrimSpace(request.Recurrence), "once") {
		return domains.Schedule{}, domains.E("INVALID_SCHEDULE", "use recurrence none for a one-time send", 400)
	}
	spec, err := validations.ParseScheduleOptions(request.ScheduleOptions, time.Now().UTC())
	if err != nil {
		return domains.Schedule{}, domains.E("INVALID_SCHEDULE", err.Error(), 400)
	}
	if request.MediaID != "" {
		if _, err = s.mediaStore.GetMedia(ctx, d.ConnectionID, request.MediaID); err != nil {
			return domains.Schedule{}, err
		}
	}
	return s.scheduleStore.CreateScheduleIdempotent(ctx, d.ConnectionID, request, spec.ScheduledAt, key)
}

func (s *ScheduleService) ListSchedules(ctx context.Context, id string, limit, offset int) ([]domains.Schedule, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	limit, offset = page(limit, offset)
	return s.scheduleStore.ListSchedules(ctx, d.ConnectionID, limit, offset)
}

func (s *ScheduleService) GetSchedule(ctx context.Context, id, scheduleID string) (domains.Schedule, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.Schedule{}, err
	}
	return s.scheduleStore.GetSchedule(ctx, d.ConnectionID, scheduleID)
}

func (s *ScheduleService) ScheduleAction(ctx context.Context, id, scheduleID, action string) error {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return err
	}
	job, err := s.scheduleStore.GetSchedule(ctx, d.ConnectionID, scheduleID)
	if err != nil {
		return err
	}
	var target string
	switch strings.ToLower(action) {
	case "pause":
		if job.State != "active" {
			return domains.E("INVALID_SCHEDULE_STATE", "only active schedules can be paused", 409)
		}
		target = "paused"
	case "resume":
		if job.State != "paused" {
			return domains.E("INVALID_SCHEDULE_STATE", "only paused schedules can be resumed", 409)
		}
		target = "active"
	case "cancel":
		if job.State != "active" && job.State != "paused" {
			return domains.E("INVALID_SCHEDULE_STATE", "schedule is already terminal", 409)
		}
		target = "cancelled"
	default:
		return domains.E("INVALID_SCHEDULE_ACTION", "action must be pause, resume or cancel", 400)
	}
	return s.scheduleStore.SetScheduleState(ctx, d.ConnectionID, scheduleID, target)
}

// ScheduleUpload accepts the same bounded file as an immediate send, but keeps
// it pinned by a durable schedule. No provider upload happens during admission.
func (s *ScheduleService) ScheduleUpload(ctx context.Context, id string, request domains.SendRequest, key string, media domains.Media, digest string) (domains.Schedule, error) {
	if request.Operation != "" || len(request.Payload) > 0 || request.RequestID != "" || request.MediaID != media.ID {
		return domains.Schedule{}, domains.E("INVALID_REQUEST", "multipart schedules require gateway-assigned media and request identities", 400)
	}
	switch request.Kind {
	case "file", "image", "audio", "video", "voice":
	default:
		return domains.Schedule{}, domains.E("INVALID_REQUEST", "multipart schedules require a media kind", 400)
	}
	if err := request.Validate(); err != nil {
		return domains.Schedule{}, err
	}
	if !request.IsScheduled() || strings.TrimSpace(key) == "" || len(key) > 256 {
		return domains.Schedule{}, domains.E("INVALID_REQUEST", "scheduled_at and a nonempty idempotency key of at most 256 bytes are required", 400)
	}
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.Schedule{}, err
	}
	if err = s.validateSend(d, request); err != nil {
		return domains.Schedule{}, err
	}
	if d.ConnectionID != media.ConnectionID {
		return domains.Schedule{}, domains.E("DEVICE_SCOPE_MISMATCH", "upload belongs to another connection", 409)
	}
	previous, found, err := s.scheduleStore.LookupScheduleUpload(ctx, d.ConnectionID, request, key, media, digest)
	if err != nil || found {
		return previous, err
	}
	if strings.EqualFold(strings.TrimSpace(request.Recurrence), "once") {
		return domains.Schedule{}, domains.E("INVALID_SCHEDULE", "use recurrence none for a one-time send", 400)
	}
	spec, err := validations.ParseScheduleOptions(request.ScheduleOptions, time.Now().UTC())
	if err != nil {
		return domains.Schedule{}, domains.E("INVALID_SCHEDULE", err.Error(), 400)
	}
	return s.scheduleStore.CreateScheduleUpload(ctx, d.ConnectionID, request, spec.ScheduledAt, key, media, digest)
}
