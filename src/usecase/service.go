// Package usecase owns provider-independent connection and delivery orchestration.
package usecase

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/workpool"
)

// Options controls bounded background work. The gateway must remain behind an
// authenticated service boundary; organization/operator authorization belongs to the consuming application.
type Options struct {
	Providers            *domains.ProviderRegistry
	GlobalWebhooks       []domains.WebhookTarget
	GlobalWebhookEvents  []string
	MergeGlobal          bool
	QueueLimit           int
	ConnectionQueueLimit int
	PollInterval         time.Duration
	WebhookClient        *http.Client
	WebhookWorkers       int
	SendWorkers          int
	SendTimeout          time.Duration
	ReconnectWorkers     int
}

type clientEntry struct {
	provider          domains.Provider
	persistMu         sync.Mutex
	persistGeneration uint64
	persistActive     bool
	persistSession    domains.SessionPersister
	mu                sync.Mutex   // serializes sends and lifecycle transitions for this account
	clientMu          sync.RWMutex // short lock for status snapshots when lifecycle work is in progress
	client            domains.Client
	authMetaMu        sync.Mutex // public challenge snapshots must not block on provider calls
	challenge         *domains.PublicChallenge
	resendAvailableAt time.Time // provider cooldown survives local challenge expiry
	desired           bool
	closed            bool
	failures          int
	nextConnect       time.Time
}

type serviceRuntime struct {
	accountStore  AccountStore
	sendStore     SendStore
	scheduleStore ScheduleStore
	eventStore    EventStore
	webhookStore  WebhookStore
	mediaStore    MediaStore
	options       Options
	factory       domains.ClientFactory
	lifecycleMu   sync.Mutex
	mu            sync.RWMutex
	clients       map[string]*clientEntry
	ctx           context.Context
	cancel        context.CancelFunc
	started       bool
	closed        bool
	wg            sync.WaitGroup
	done          chan struct{}
	reconnectPool *workpool.Pool
	metrics       workerMetrics
}

func New(store Store, options Options) *Service {
	originalRegistry := options.Providers
	factory := func(d domains.Device) domains.Client {
		r, err := originalRegistry.Get(d.Provider)
		if err != nil || r.Factory == nil {
			return nil
		}
		return r.Factory(d)
	}
	if options.QueueLimit <= 0 {
		options.QueueLimit = 1000
	}
	if options.ConnectionQueueLimit <= 0 {
		options.ConnectionQueueLimit = 100
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 500 * time.Millisecond
	}
	if options.WebhookWorkers <= 0 {
		options.WebhookWorkers = 8
	}
	if options.SendWorkers <= 0 {
		options.SendWorkers = 4
	}
	if options.ReconnectWorkers <= 0 {
		options.ReconnectWorkers = 4
	}
	if options.ReconnectWorkers > 4 {
		options.ReconnectWorkers = 4
	}
	if options.SendTimeout <= 0 {
		options.SendTimeout = 40 * time.Second
	}
	if options.WebhookClient == nil {
		options.WebhookClient = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	} else {
		copyClient := *options.WebhookClient
		if copyClient.Timeout <= 0 || copyClient.Timeout > 10*time.Second {
			copyClient.Timeout = 10 * time.Second
		}
		copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		options.WebhookClient = &copyClient
	}
	if store != nil {
		store.SetSendConcurrency(options.SendWorkers)
	}
	runtime := &serviceRuntime{
		accountStore: store, sendStore: store, scheduleStore: store,
		eventStore: store, webhookStore: store, mediaStore: store,
		options: options, factory: factory,
		clients: make(map[string]*clientEntry), done: make(chan struct{}),
		reconnectPool: workpool.New(options.ReconnectWorkers),
	}
	return &Service{
		serviceRuntime:  runtime,
		AccountService:  &AccountService{serviceRuntime: runtime, accountStore: store, webhookStore: store},
		SendService:     &SendService{serviceRuntime: runtime, sendStore: store},
		ScheduleService: &ScheduleService{serviceRuntime: runtime, scheduleStore: store, mediaStore: store},
		QueryService:    &QueryService{serviceRuntime: runtime, eventStore: store},
		WebhookService:  &WebhookService{serviceRuntime: runtime, webhookStore: store},
		MediaService:    &MediaService{serviceRuntime: runtime, mediaStore: store, sendStore: store},
	}
}

func (s *serviceRuntime) Start(ctx context.Context) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return domains.E("SERVICE_CLOSED", "service is closed", 503)
	}
	if s.started {
		s.mu.Unlock()
		return nil
	}
	if s.accountStore == nil || s.options.Providers == nil || s.factory == nil {
		s.mu.Unlock()
		return domains.E("SERVICE_CONFIGURATION", "store and provider factory are required", 500)
	}
	s.mu.Unlock()
	devices, err := s.accountStore.ListDevices(ctx)
	if err != nil {
		return err
	}
	for _, device := range devices {
		if _, err := s.provider(device.Provider); err != nil {
			continue
		}
		entry, err := s.entry(device)
		if err != nil {
			return err
		}
		entry.mu.Lock()
		session, loadErr := s.accountStore.LoadSession(ctx, device.ConnectionID)
		if loadErr == nil {
			entry.desired = session != nil
			entry.setPersistenceActive(session != nil)
		}
		entry.mu.Unlock()
		if loadErr != nil {
			return loadErr
		}
	}
	s.mu.Lock()
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.started = true
	s.mu.Unlock()
	for n := 0; n < s.options.SendWorkers; n++ {
		s.wg.Add(1)
		go s.sendLoop()
	}
	for n := 0; n < s.options.WebhookWorkers; n++ {
		s.wg.Add(1)
		go s.webhookLoop()
	}
	s.wg.Add(2)
	go s.scheduleLoop()
	go s.reconnectLoop()
	return nil
}

func (s *serviceRuntime) Close(ctx context.Context) error {
	s.lifecycleMu.Lock()
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		if s.cancel != nil {
			s.cancel()
		}
		entries := make([]*clientEntry, 0, len(s.clients))
		for _, entry := range s.clients {
			entries = append(entries, entry)
		}
		go func() {
			s.wg.Wait()
			var disconnect sync.WaitGroup
			for _, entry := range entries {
				disconnect.Add(1)
				go func(e *clientEntry) {
					defer disconnect.Done()
					e.mu.Lock()
					defer e.mu.Unlock()
					e.closed = true
					e.clearAuthMetadata()
					e.desired = false
					e.setPersistenceActive(false)
					callCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = e.client.Disconnect(callCtx)
				}(entry)
			}
			disconnect.Wait()
			close(s.done)
		}()
	}
	s.mu.Unlock()
	s.lifecycleMu.Unlock()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *serviceRuntime) entry(device domains.Device) (*clientEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, domains.E("SERVICE_CLOSED", "service is closed", 503)
	}
	if s.factory == nil {
		return nil, domains.E("SERVICE_CONFIGURATION", "provider factory is required", 500)
	}
	entry := s.clients[device.ConnectionID]
	if entry == nil {
		if _, err := s.provider(device.Provider); err != nil {
			return nil, err
		}
		entry = &clientEntry{provider: device.Provider, client: s.factory(device), persistSession: func(ctx context.Context, next *domains.Session) error {
			return s.accountStore.UpdateSession(ctx, device.ConnectionID, next)
		}}
		if entry.client == nil {
			return nil, domains.E("SERVICE_CONFIGURATION", "provider factory returned no client", 500)
		}
		entry.attachSessionPersistence(entry.client)
		s.clients[device.ConnectionID] = entry
	}
	return entry, nil
}
func (s *serviceRuntime) ResolveDevice(ctx context.Context, id string) (domains.Device, error) {
	if selected, ok := ctx.Value(deviceScopeKey{}).(deviceScope); ok {
		if id != "" && id != selected.alias {
			return domains.Device{}, domains.E("DEVICE_SCOPE_MISMATCH", "request is bound to another device", 409)
		}
		return s.accountStore.DeviceByConnection(ctx, selected.connection)
	}
	if id != "" {
		return s.accountStore.GetDevice(ctx, id)
	}
	devices, err := s.accountStore.ListDevices(ctx)
	if err != nil {
		return domains.Device{}, err
	}
	if len(devices) != 1 {
		return domains.Device{}, domains.E("DEVICE_ID_REQUIRED", "select a device explicitly", 400)
	}
	return devices[0], nil
}

func validateWebhookURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return domains.E("INVALID_WEBHOOK", "webhook_url must be an HTTP(S) URL without credentials or fragment", 400)
	}
	return nil
}

// Routing plans are resolved against current device configuration inside storage.
func (s *serviceRuntime) targets(_ domains.Device, event string) []domains.WebhookTarget {
	result := []domains.WebhookTarget{{Device: true, CurrentDevice: true, MergeGlobal: s.options.MergeGlobal}}
	if domains.EventAllowed(event, s.options.GlobalWebhookEvents) {
		result = append(result, s.options.GlobalWebhooks...)
	}
	return result
}
func (s *serviceRuntime) sink(connection string) domains.Sink {
	return func(ctx context.Context, event domains.Event) error {
		d, err := s.accountStore.DeviceByConnection(ctx, connection)
		if err != nil {
			return err
		}
		if event.ID == "" || event.Type == "" {
			return domains.E("INVALID_EVENT", "provider event must have a stable id and type", 502)
		}
		if event.AccountID != "" && d.AccountID != "" && event.AccountID != d.AccountID {
			return domains.E("EVENT_ACCOUNT_MISMATCH", "event account does not match connection", 409)
		}
		if event.Provider != "" && event.Provider != d.Provider {
			return domains.E("EVENT_PROVIDER_MISMATCH", "event provider does not match connection", 409)
		}
		event.Provider = d.Provider
		if err := event.ValidateMessageProjection(); err != nil {
			return err
		}
		event.SessionID = d.ID
		event.AccountID = d.AccountID
		if event.Time.IsZero() {
			event.Time = time.Now().UTC()
		}
		if len(event.Payload) == 0 {
			event.Payload = json.RawMessage(`{}`)
		}
		if !json.Valid(event.Payload) {
			return domains.E("INVALID_EVENT", "provider event payload is not valid JSON", 502)
		}
		_, err = s.eventStore.AppendEvent(ctx, connection, event, s.targets(d, event.Type))
		return err
	}
}

func page(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// Provider error text is not a safe public or persisted diagnostic: upstream
// responses can contain URLs, tokens or echoed credentials. Codes are retained.
func safeError(err error) error {
	if err == nil {
		return nil
	}
	var de *domains.Error
	if errors.As(err, &de) {
		copyErr := *de
		copyErr.Code = safeCode(copyErr.Code)
		copyErr.Message = publicErrorMessage(copyErr.Code)
		return &copyErr
	}
	if errors.Is(err, context.Canceled) {
		return domains.E("REQUEST_CANCELLED", "request was cancelled", 499)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &domains.Error{Code: "PROVIDER_TIMEOUT", Message: "provider request timed out", HTTP: 504}
	}
	return domains.E("PROVIDER_ERROR", "provider request failed", 502)
}
func publicErrorMessage(code string) string {
	switch code {
	case "FEATURE_NOT_SUPPORTED":
		return "feature is not verified for this provider"
	case "SEND_UNKNOWN":
		return "send outcome is unknown; do not resend without reconciliation"
	case "AUTH_REQUIRED":
		return "account authentication is required"
	case "INVALID_CODE", "AUTH_CODE_INVALID":
		return "authentication code is invalid"
	case "INVALID_PASSWORD", "AUTH_PASSWORD_INVALID":
		return "authentication password is invalid"
	case "CHALLENGE_EXPIRED":
		return "authentication challenge expired"
	case "AUTH_RESEND_TOO_SOON":
		return "wait before requesting another authentication code"
	case "CONNECTION_UNAVAILABLE":
		return "provider connection is unavailable"
	default:
		return "provider request failed (" + safeCode(code) + ")"
	}
}
func safeCode(code string) string {
	if code == "" || len(code) > 80 {
		return "PROVIDER_ERROR"
	}
	for _, r := range code {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return "PROVIDER_ERROR"
		}
	}
	return code
}
func cleanStatus(status domains.ConnectionStatus) domains.ConnectionStatus {
	switch status.LastError {
	case "", "CONNECT_FAILED", "CONNECTION_LOST", "SESSION_REVOKED", "HANDSHAKE_FAILED", "WRITE_FAILED", "PROTOCOL_ERROR", "UPDATE_PROTOCOL_ERROR", "UPDATE_ACCEPTANCE_FAILED", "HEARTBEAT_TIMEOUT", "HEARTBEAT_FAILED", "EVENT_CONTEXT_MISSING", "EVENT_PERSIST_FAILED", "EVENT_BACKPRESSURE", "EVENT_DRAIN_INCOMPLETE", "RECOVERY_FAILED", "RECOVERY_GAP":
		return status
	}
	for _, prefix := range []string{"WS_CLOSE_", "CONNECTION_CLOSED_"} {
		if strings.HasPrefix(status.LastError, prefix) && len(status.LastError) == len(prefix)+4 && strings.Trim(strings.TrimPrefix(status.LastError, prefix), "0123456789") == "" {
			return status
		}
	}
	status.LastError = "provider reported a connection error"
	return status
}
func reconnectDelay(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	if failures > 6 {
		failures = 6
	}
	base := time.Duration(1<<uint(failures-1)) * time.Second
	var b [8]byte
	_, _ = rand.Read(b[:])
	return base + time.Duration(binary.LittleEndian.Uint64(b[:])%uint64(base/4+1))
}
func (s *serviceRuntime) wait() bool {
	timer := time.NewTimer(s.options.PollInterval)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func diagnostic(err error) string {
	if err == nil {
		return ""
	}
	var de *domains.Error
	if errors.As(err, &de) {
		return fmt.Sprintf("%s: %s", safeCode(de.Code), publicErrorMessage(de.Code))
	}
	return "provider operation failed"
}

func (e *clientEntry) setClient(client domains.Client) {
	e.attachSessionPersistence(client)
	e.clearAuthMetadata()
	e.clientMu.Lock()
	e.client = client
	e.clientMu.Unlock()
}
