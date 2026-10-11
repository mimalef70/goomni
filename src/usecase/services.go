package usecase

// Service composes feature services over one client/worker runtime. It does not
// create another queue, connection owner or persistence implementation.
type Service struct {
	*serviceRuntime
	*AccountService
	*SendService
	*ScheduleService
	*QueryService
	*WebhookService
	*MediaService
}
type AccountService struct {
	*serviceRuntime
	accountStore AccountStore
	webhookStore WebhookStore
}
type SendService struct {
	*serviceRuntime
	sendStore SendStore
}
type ScheduleService struct {
	*serviceRuntime
	scheduleStore ScheduleStore
	mediaStore    MediaStore
}
type QueryService struct {
	*serviceRuntime
	eventStore EventStore
}
type WebhookService struct {
	*serviceRuntime
	webhookStore WebhookStore
}
type MediaService struct {
	*serviceRuntime
	mediaStore MediaStore
	sendStore  SendStore
}
