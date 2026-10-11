package usecase

import (
	"context"
	"encoding/json"
	"io"

	"github.com/mimalef70/goomni/src/domains"
)

type AccountAPI interface {
	CreateDevice(ctx context.Context, id string, provider domains.Provider) (domains.Device, error)
	ListDevices(ctx context.Context) ([]domains.Device, error)
	GetDevice(ctx context.Context, id string) (domains.Device, error)
	DeleteDevice(ctx context.Context, id string) error
	StartAuth(ctx context.Context, id, phone string) (domains.Challenge, error)
	SubmitCode(ctx context.Context, id, challenge, code string) (domains.ConnectionStatus, error)
	SubmitPassword(ctx context.Context, id, challenge, password string) (domains.ConnectionStatus, error)
	Reconnect(ctx context.Context, id string) error
	Logout(ctx context.Context, id string) error
	DevicesOverview(ctx context.Context) (domains.DevicesOverview, error)
	LoginState(ctx context.Context, id string) (domains.LoginState, error)
	Status(ctx context.Context, id string) (domains.DeviceStatus, error)
	ProvisionDevice(ctx context.Context, request domains.ProvisionDeviceRequest, key string) (domains.Device, bool, error)
}

var _ AccountAPI = (*AccountService)(nil)

type SendAPI interface {
	Call(ctx context.Context, id, operation string, payload json.RawMessage) (json.RawMessage, error)
	ListOperationsFiltered(ctx context.Context, id string, f domains.OperationFilter) ([]domains.Operation, error)
	Mutate(ctx context.Context, id, operation string, payload json.RawMessage, key string) (domains.Operation, error)
	ValidateSend(ctx context.Context, id string, r domains.SendRequest) error
	Send(ctx context.Context, id string, request domains.SendRequest, key string) (domains.Operation, error)
	GetOperation(ctx context.Context, id, operationID string) (domains.Operation, error)
}

var _ SendAPI = (*SendService)(nil)

type ScheduleAPI interface {
	ScheduleUpload(ctx context.Context, id string, request domains.SendRequest, key string, media domains.Media, digest string) (domains.Schedule, error)
	ListSchedulesFiltered(ctx context.Context, id string, f domains.ScheduleFilter) ([]domains.Schedule, error)
	ListScheduleOccurrences(ctx context.Context, id, scheduleID, state string, limit, offset int) ([]domains.ScheduleOccurrence, error)
	CreateSchedule(ctx context.Context, id string, request domains.SendRequest) (domains.Schedule, error)
	CreateScheduleIdempotent(ctx context.Context, id string, request domains.SendRequest, key string) (domains.Schedule, error)
	ListSchedules(ctx context.Context, id string, limit, offset int) ([]domains.Schedule, error)
	GetSchedule(ctx context.Context, id, scheduleID string) (domains.Schedule, error)
	ScheduleAction(ctx context.Context, id, scheduleID, action string) error
}

var _ ScheduleAPI = (*ScheduleService)(nil)

type QueryAPI interface {
	Chats(ctx context.Context, id string, limit, offset int) ([]domains.Chat, error)
	Messages(ctx context.Context, id, peer string, limit, offset int) ([]domains.Event, error)
	EventsFiltered(ctx context.Context, id string, f domains.EventFilter) ([]domains.Event, error)
}

var _ QueryAPI = (*QueryService)(nil)

type WebhookAPI interface {
	GetWebhook(ctx context.Context, id string) (domains.WebhookConfig, error)
	PatchWebhook(ctx context.Context, id string, patch domains.WebhookPatch) (domains.WebhookConfig, error)
	WebhookDetails(ctx context.Context, id string) (domains.WebhookDetails, error)
	ListDeliveriesFiltered(ctx context.Context, id string, limit, offset int, state string, includePayload bool) ([]domains.Delivery, error)
	ListDeliveries(ctx context.Context, id string, limit, offset int) ([]domains.Delivery, error)
	GetDelivery(ctx context.Context, id, deliveryID string) (domains.Delivery, error)
	ReplayDelivery(ctx context.Context, id, deliveryID string) ([]domains.Delivery, error)
	RetryDelivery(ctx context.Context, id, deliveryID string) error
}

var _ WebhookAPI = (*WebhookService)(nil)

type MediaAPI interface {
	RegisterMedia(context.Context, string, domains.Media) error
	LocalMedia(context.Context, string, string) (domains.Media, error)
	MediaFileRegistered(context.Context, string, string, string, string) (bool, error)

	SendUpload(ctx context.Context, id string, request domains.SendRequest, key string, media domains.Media, digest string) (domains.Operation, error)
	DownloadAvatar(ctx context.Context, id string, user domains.Peer, size string) (io.ReadCloser, domains.AvatarInfo, error)
	Download(ctx context.Context, id string, peer domains.Peer, messageID string) (io.ReadCloser, domains.ProviderMedia, error)
}

var _ MediaAPI = (*MediaService)(nil)

type RuntimeAPI interface {
	ResolveDevice(ctx context.Context, id string) (domains.Device, error)
	WorkerMetrics() Metrics
	BindDevice(ctx context.Context, device domains.Device) (context.Context, error)
	Providers() []domains.ProviderDescriptor
	Capabilities(p domains.Provider) ([]domains.OperationContract, error)
	DeviceCapabilities(ctx context.Context, id string) ([]domains.OperationContract, error)
	ActiveProviders(ctx context.Context) ([]domains.Provider, error)
}

// Gateway is the HTTP composition port. HTTP has no access to client or SQL internals.
type Gateway interface {
	RuntimeAPI
	AccountAPI
	SendAPI
	ScheduleAPI
	QueryAPI
	WebhookAPI
	MediaAPI
}

var _ Gateway = (*Service)(nil)
