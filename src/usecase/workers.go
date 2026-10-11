package usecase

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

func (s *serviceRuntime) sendLoop() {
	defer s.wg.Done()
	for s.ctx.Err() == nil {
		jobs, err := s.sendStore.ClaimOperations(s.ctx, 1)
		s.workerError(err)
		if err == nil && len(jobs) > 0 {
			s.processOperation(jobs[0])
			continue
		}
		if !s.wait() {
			return
		}
	}
}
func (s *serviceRuntime) processOperation(op domains.Operation) {
	defer s.busyWorker("send")()
	// Every exit is persisted with a detached short context: shutdown must not
	// leave an accepted provider send eligible for blind automatic retry.
	finish := func(state string, result *domains.SendResult, code, message string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := s.sendStore.FinishOperation(ctx, op.ConnectionID, op.ID, state, result, code, message)
		var conflict *domains.Error
		if errors.As(err, &conflict) && conflict.Code == "OPERATION_CONFLICT" {
			// A persisted native own-message echo can prove success before the
			// send RPC finishes. Never downgrade it or count the race as failure.
			current, lookupErr := s.sendStore.GetOperation(ctx, op.ConnectionID, op.ID)
			if lookupErr == nil && current.State == "succeeded" {
				return
			}
		}
		s.workerError(err)
	}
	if s.ctx.Err() != nil {
		finish("queued", nil, "", "")
		return
	}
	d, err := s.accountStore.DeviceByConnection(s.ctx, op.ConnectionID)
	if err != nil {
		var missing *domains.Error
		if errors.As(err, &missing) && (missing.Code == "DEVICE_NOT_FOUND" || missing.Code == "NOT_FOUND") {
			finish("failed", nil, "DEVICE_NOT_FOUND", "connection no longer exists")
		} else {
			s.workerError(err)
			finish("queued", nil, "", "") // no provider request has been made
			s.wait()
		}
		return
	}
	e, err := s.entry(d)
	if err != nil {
		finish("queued", nil, "", "")
		// Disabling an adapter does not invalidate already accepted work. Wait
		// before claiming again so an unavailable adapter cannot spin the queue.
		s.wait()
		return
	}
	e.mu.Lock()
	pause := false
	defer func() {
		e.mu.Unlock()
		if pause {
			s.wait()
		}
	}()
	if e.closed {
		finish("failed", nil, "DEVICE_NOT_FOUND", "connection no longer exists")
		return
	}
	// A logout or deletion can invalidate a claim while this worker waits for
	// the account lock. Recheck the durable state before any provider call.
	current, lookupErr := s.sendStore.GetOperation(s.ctx, op.ConnectionID, op.ID)
	if lookupErr != nil {
		s.workerError(lookupErr)
		finish("queued", nil, "", "") // this worker has not invoked the provider
		return
	}
	if current.State != "sending" {
		return
	}
	state := e.client.Status()
	if state.Auth == "auth_required" || state.Auth == "logged_out" || (!e.desired && state.Auth == "unauthenticated") {
		finish("failed", nil, "AUTH_REQUIRED", "account authentication is required")
		return
	}
	if state.Transport != "connected" {
		finish("queued", nil, "", "")
		pause = true
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.options.SendTimeout)
	ctx = domains.WithOperationStageRecorder(ctx, func(stageCtx context.Context, stage domains.OperationStage) error {
		return s.sendStore.RecordOperationStage(stageCtx, op.ConnectionID, op.ID, op.Request.RequestID, stage)
	})
	started := time.Now()
	var result domains.SendResult
	if op.Request.Kind == "operation" {
		var payload []byte
		payload, err = mutationWirePayload(op.Request)
		if err == nil {
			result.Data, err = e.client.Call(ctx, op.Request.Operation, payload)
		}
		if err == nil && (len(result.Data) > 1<<20 || !json.Valid(result.Data)) {
			err = &domains.Error{Code: "SEND_UNKNOWN", Message: "mutation response was invalid", HTTP: 202, Ambiguous: true}
		}
	} else {
		result, err = e.client.Send(ctx, op.Request)
	}
	cancel()
	var sendErr *domains.Error
	unknown := err != nil && (!errors.As(err, &sendErr) || sendErr.Ambiguous)
	s.observeSend(started, unknown)
	if err == nil {
		finish("succeeded", &result, "", "")
		return
	}
	var de *domains.Error
	if errors.As(err, &de) {
		if de.Ambiguous {
			finish("unknown", nil, "SEND_UNKNOWN", publicErrorMessage("SEND_UNKNOWN"))
			return
		}
		// Only the provider's explicit before-wire classification can requeue.
		if de.Retryable && de.Code == "CONNECTION_UNAVAILABLE" {
			finish("queued", nil, "CONNECTION_UNAVAILABLE", publicErrorMessage(de.Code))
			pause = true
			return
		}
		finish("failed", nil, safeCode(de.Code), publicErrorMessage(de.Code))
		return
	}
	// A generic timeout/error after invoking Send cannot prove that nothing left
	// the socket. Never classify it as safely retryable.
	finish("unknown", nil, "SEND_UNKNOWN", publicErrorMessage("SEND_UNKNOWN"))
}

func (s *serviceRuntime) reconnectLoop() {
	defer s.wg.Done()
	cursor := reconnectCursor{connections: make(map[domains.Provider]string)}
	for s.ctx.Err() == nil {
		s.reconnectPass(&cursor)
		if !s.wait() {
			return
		}
	}
}

// Nonblocking pool attempts have no queued waiter after they fail. Keep a
// dispatch cursor across polls to fairly rotate providers and their connections.
type reconnectCursor struct {
	provider    domains.Provider
	connections map[domains.Provider]string
}

func (s *serviceRuntime) reconnectPass(cursor *reconnectCursor) {
	providers, err := s.ActiveProviders(s.ctx)
	if err != nil {
		s.workerError(err)
		return
	}
	s.reconnectPool.SetProviders(providers)
	if len(providers) == 0 {
		return
	}
	if cursor.connections == nil {
		cursor.connections = make(map[domains.Provider]string)
	}
	s.mu.RLock()
	entries := make(map[string]*clientEntry, len(s.clients))
	connections := make(map[domains.Provider][]string, len(providers))
	for conn, e := range s.clients {
		entries[conn] = e
		connections[e.provider] = append(connections[e.provider], conn)
	}
	s.mu.RUnlock()
	for provider, ids := range connections {
		sort.Strings(ids)
		start := sort.SearchStrings(ids, cursor.connections[provider])
		if start < len(ids) && ids[start] == cursor.connections[provider] {
			start++
		}
		connections[provider] = append(append([]string(nil), ids[start:]...), ids[:start]...)
	}
	start := 0
	for i, provider := range providers {
		if provider == cursor.provider {
			start = (i + 1) % len(providers)
			break
		}
	}
	providers = append(append([]domains.Provider(nil), providers[start:]...), providers[:start]...)
	launched := 0
	for launched < s.options.ReconnectWorkers {
		progress := false
		for _, provider := range providers {
			for len(connections[provider]) > 0 {
				conn := connections[provider][0]
				connections[provider] = connections[provider][1:]
				e := entries[conn]
				if !e.mu.TryLock() {
					continue
				}
				if e.closed || !e.desired {
					e.mu.Unlock()
					continue
				}
				status := e.client.Status()
				if status.Auth == "auth_required" {
					e.desired = false
					e.setPersistenceActive(false)
					s.workerError(s.accountStore.ClearSession(s.ctx, conn))
					e.mu.Unlock()
					continue
				}
				if status.Transport == "connected" {
					e.failures = 0
					e.mu.Unlock()
					continue
				}
				if time.Now().Before(e.nextConnect) {
					e.mu.Unlock()
					continue
				}
				release, acquired := s.reconnectPool.TryAcquireFor(provider, conn)
				if !acquired {
					e.mu.Unlock()
					// Other connections cannot bypass this provider's quota.
					connections[provider] = nil
					break
				}
				cursor.provider, cursor.connections[provider] = provider, conn
				launched++
				progress = true
				s.wg.Add(1)
				go s.connectEntry(conn, e, release) // entry lock transfers to the worker
				break
			}
			if launched == s.options.ReconnectWorkers {
				return
			}
		}
		if !progress {
			return
		}
	}
}
func (s *serviceRuntime) connectEntry(conn string, e *clientEntry, release func()) {
	defer s.wg.Done()
	defer s.busyWorker("reconnect")()
	defer release()
	defer e.mu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	session, err := s.stopForReconnect(ctx, conn, e)
	if err == nil && session == nil {
		e.desired = false
		return
	}
	if err == nil {
		started := time.Now()
		var d domains.Device
		d, err = s.accountStore.DeviceByConnection(ctx, conn)
		if err == nil {
			e.setPersistenceActive(true)
			err = s.connect(ctx, d, e.client, session)
		}
		s.observeReconnect(started, err != nil && s.ctx.Err() == nil)
	}
	if err != nil {
		e.failures++
		e.nextConnect = time.Now().Add(reconnectDelay(e.failures))
	} else {
		e.failures = 0
		e.nextConnect = time.Time{}
	}
}

func (s *serviceRuntime) webhookLoop() {
	defer s.wg.Done()
	for s.ctx.Err() == nil {
		jobs, err := s.webhookStore.ClaimDeliveries(s.ctx, 1, time.Now().UTC())
		s.workerError(err)
		if err == nil && len(jobs) > 0 {
			s.deliver(jobs[0])
			continue
		}
		if !s.wait() {
			return
		}
	}
}
func (s *serviceRuntime) deliver(d domains.Delivery) {
	defer s.busyWorker("webhook")()
	update := func(state string, next time.Time, message string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.workerError(s.webhookStore.UpdateDelivery(ctx, d.ConnectionID, d.ID, state, next, message))
	}
	if err := validateWebhookURL(d.URL); err != nil {
		update("failed", time.Time{}, "invalid webhook URL")
		return
	}
	device, err := s.accountStore.DeviceByConnection(s.ctx, d.ConnectionID)
	if err != nil {
		var missing *domains.Error
		if errors.As(err, &missing) && missing.Code == "NOT_FOUND" {
			// DeleteDevice atomically cancels its delivery ledger. A stale
			// claim must not revive it or rewrite that terminal audit state.
			return
		}
		if s.ctx.Err() == nil {
			s.workerError(err)
		}
		// Cancellation or a transient DB failure does not revoke the target.
		// Keep the claim recoverable instead of silently pausing a valid event.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		s.workerError(s.webhookStore.ReleaseDeliveryClaim(ctx, d.ConnectionID, d.ID))
		cancel()
		s.wait()
		return
	}
	// Device-target revisions identify the authorization to send to a URL. A URL
	// change must never silently forward old queued payloads to its replacement.
	if d.Revision != device.Webhook.Revision || (d.Device && d.URL != device.Webhook.URL) {
		update("paused", time.Time{}, "webhook target changed; replay explicitly to current targets")
		return
	}
	if d.Device {
		d.Secret = device.Webhook.Secret
	} else {
		found := false
		for _, target := range s.options.GlobalWebhooks {
			if target.URL == d.URL {
				d.Secret = target.Secret
				found = true
				break
			}
		}
		if !found {
			update("paused", time.Time{}, "global webhook target is no longer configured; replay explicitly")
			return
		}
	}
	if strings.TrimSpace(d.Secret) == "" {
		update("failed", time.Time{}, "webhook secret is missing; configure a secret before retrying")
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, strings.NewReader(string(d.Body)))
	if err != nil {
		update("failed", time.Time{}, "invalid webhook request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GoOmni-Event-Id", d.EventID)
	req.Header.Set("X-GoOmni-Delivery-Id", d.ID)
	req.Header.Set("X-Webhook-Id", d.EventID)
	req.Header.Set("User-Agent", "GoOmni-Webhook/1")
	mac := hmac.New(sha256.New, []byte(d.Secret))
	_, _ = mac.Write(d.Body)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	started := time.Now()
	response, err := s.options.WebhookClient.Do(req)
	successful := false
	if err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		_ = response.Body.Close()
		successful = response.StatusCode >= 200 && response.StatusCode < 300
	}
	s.observeWebhook(started, !successful)
	if successful {
		update("delivered", time.Time{}, "")
		return
	}
	if s.ctx.Err() != nil {
		update("retry", time.Now().UTC(), "service shutdown interrupted delivery; retry pending")
		return
	}
	// A receiver can reject one event permanently without holding up all later
	// work for this destination. Keep the failed ledger row and immutable body
	// for inspection and explicit retry/replay; never record it as delivered.
	if err == nil && permanentWebhookStatus(response.StatusCode) {
		update("failed", time.Time{}, "webhook endpoint permanently rejected delivery (HTTP "+strconv.Itoa(response.StatusCode)+"); explicit retry or replay required")
		return
	}
	if d.Attempts >= 8 {
		update("failed", time.Time{}, "webhook delivery failed after 8 attempts")
		return
	}
	delay := webhookRetryDelay(d.Attempts)
	update("retry", time.Now().UTC().Add(delay), "webhook endpoint did not acknowledge delivery")
}

func permanentWebhookStatus(status int) bool {
	return status >= 400 && status < 500 && status != http.StatusRequestTimeout && status != http.StatusTooEarly && status != http.StatusTooManyRequests
}

func webhookRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 8 {
		attempt = 8
	}
	ceiling := time.Duration(1<<uint(attempt)) * time.Second
	// Equal jitter avoids synchronized retries while retaining a nonzero floor.
	return ceiling/2 + time.Duration(rand.Int64N(int64(ceiling/2)+1))
}
