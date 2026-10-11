package usecase

import (
	"context"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

// Ports expose complete durable operations, never SQL transactions. The one
// SQLite Store implements these independent application-owned contracts.
type AccountStore interface {
	CreateDevice(ctx context.Context, id string, provider domains.Provider) (domains.Device, error)
	ListDevices(ctx context.Context) ([]domains.Device, error)
	GetDevice(ctx context.Context, id string) (domains.Device, error)
	DeviceByConnection(ctx context.Context, conn string) (domains.Device, error)
	DeleteDevice(ctx context.Context, conn string) error
	SaveSession(ctx context.Context, conn string, session *domains.Session) error
	UpdateSession(ctx context.Context, conn string, session *domains.Session) error
	LoadSession(ctx context.Context, conn string) (*domains.Session, error)
	ClearSession(ctx context.Context, conn string) error
	LogoutConnection(ctx context.Context, conn string) error
	ProvisionDevice(ctx context.Context, request domains.ProvisionDeviceRequest, key string) (domains.Device, bool, error)
}
type SendStore interface {
	Enqueue(ctx context.Context, conn string, req domains.SendRequest, key string, limits domains.AdmissionLimits) (op domains.Operation, created bool, err error)
	EnqueueUpload(ctx context.Context, conn string, req domains.SendRequest, key string, media domains.Media, digest string, limits domains.AdmissionLimits) (op domains.Operation, err error)
	GetOperation(ctx context.Context, conn, id string) (domains.Operation, error)
	OperationStages(ctx context.Context, conn, operation string) ([]domains.OperationStage, error)
	RecordOperationStage(ctx context.Context, conn, operation, requestID string, stage domains.OperationStage) error
	ClaimOperations(ctx context.Context, limit int) (ops []domains.Operation, err error)
	FinishOperation(ctx context.Context, conn, id, state string, result *domains.SendResult, code, message string) (err error)
	ListOperationsFiltered(ctx context.Context, conn string, f domains.OperationFilter) ([]domains.Operation, error)
	SetSendConcurrency(workers int)
}
type ScheduleStore interface {
	LookupScheduleIdempotent(ctx context.Context, conn string, request domains.SendRequest, key string) (domains.Schedule, bool, error)
	CreateScheduleIdempotent(ctx context.Context, conn string, request domains.SendRequest, next time.Time, key string) (schedule domains.Schedule, err error)
	GetSchedule(ctx context.Context, conn, id string) (domains.Schedule, error)
	ListSchedules(ctx context.Context, conn string, limit, offset int) ([]domains.Schedule, error)
	SetScheduleState(ctx context.Context, conn, id, state string) error
	DueSchedules(ctx context.Context, at time.Time, limit int, afterNextAt time.Time, afterID string) ([]domains.Schedule, error)
	MaterializeSchedule(ctx context.Context, conn, id string, expected time.Time, next *time.Time, limits domains.AdmissionLimits) (op domains.Operation, err error)
	ListSchedulesFiltered(ctx context.Context, conn string, f domains.ScheduleFilter) ([]domains.Schedule, error)
	ListScheduleOccurrences(ctx context.Context, conn, id, state string, limit, offset int) ([]domains.ScheduleOccurrence, error)
	LookupScheduleUpload(ctx context.Context, conn string, req domains.SendRequest, key string, media domains.Media, digest string) (domains.Schedule, bool, error)
	CreateScheduleUpload(ctx context.Context, conn string, req domains.SendRequest, next time.Time, key string, media domains.Media, digest string) (job domains.Schedule, err error)
}
type EventStore interface {
	AppendEvent(ctx context.Context, conn string, event domains.Event, targets []domains.WebhookTarget) (created bool, err error)
	AppendBatch(ctx context.Context, conn string, batch domains.EventBatch, targets []domains.WebhookTarget) (created int, err error)
	ListChats(ctx context.Context, conn string, limit, offset int) ([]domains.Chat, error)
	ListEvents(ctx context.Context, conn, peerKey string, limit, offset int) ([]domains.Event, error)
	ListEventsFiltered(ctx context.Context, conn string, f domains.EventFilter) ([]domains.Event, error)
}
type WebhookStore interface {
	PatchWebhook(ctx context.Context, conn string, p domains.WebhookPatch) (domains.WebhookConfig, error)
	DeliveryCounts(ctx context.Context) (map[string]domains.DeliveryCounts, error)
	ListDeliveriesFiltered(ctx context.Context, conn string, limit, offset int, state string, includePayload bool) ([]domains.Delivery, error)
	ListDeliveries(ctx context.Context, conn string, limit, offset int) ([]domains.Delivery, error)
	GetDelivery(ctx context.Context, conn, id string) (domains.Delivery, error)
	ReplayDelivery(ctx context.Context, conn, id string, targets []domains.WebhookTarget) ([]domains.Delivery, error)
	RetryDelivery(ctx context.Context, conn, id string) (domains.Delivery, error)
	ClaimDeliveries(ctx context.Context, limit int, at time.Time) (deliveries []domains.Delivery, err error)
	UpdateDelivery(ctx context.Context, conn, id, state string, next time.Time, lastError string) (err error)
	ReleaseDeliveryClaim(ctx context.Context, conn, id string) error
}
type MediaStore interface {
	SaveMedia(ctx context.Context, v domains.Media) error
	GetMedia(ctx context.Context, conn, id string) (domains.Media, error)
	GetProviderMedia(ctx context.Context, conn string, peer domains.Peer, messageID string) (domains.ProviderMedia, error)
	MediaFileRegistered(ctx context.Context, conn, id, relative, absolute string) (bool, error)
}

// Store is the composition boundary; feature services consume narrower ports.
type Store interface {
	AccountStore
	SendStore
	ScheduleStore
	EventStore
	WebhookStore
	MediaStore
}
