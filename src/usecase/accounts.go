package usecase

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

// DevicesOverview is a local-only snapshot. Polling it must not instantiate a
// provider client, connect an account or issue any provider RPC.
func (s *AccountService) DevicesOverview(ctx context.Context) (domains.DevicesOverview, error) {
	devices, err := s.accountStore.ListDevices(ctx)
	if err != nil {
		return domains.DevicesOverview{}, err
	}
	counts, err := s.webhookStore.DeliveryCounts(ctx)
	if err != nil {
		return domains.DevicesOverview{}, err
	}
	now := time.Now().UTC()
	result := domains.DevicesOverview{ServerTime: now, Devices: make([]domains.DeviceOverview, 0, len(devices))}
	for _, d := range devices {
		status := domains.ConnectionStatus{Auth: "auth_required", Transport: "disconnected", Recovery: "degraded"}
		s.mu.RLock()
		e := s.clients[d.ConnectionID]
		s.mu.RUnlock()
		if e != nil {
			status = e.adminStatus(now)
		}
		result.Devices = append(result.Devices, domains.DeviceOverview{Device: d, Status: status, Deliveries: counts[d.ConnectionID]})
	}
	return result, nil
}

func (s *AccountService) LoginState(ctx context.Context, id string) (domains.LoginState, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.LoginState{}, err
	}
	now := time.Now().UTC()
	result := domains.LoginState{State: "auth_required", ServerTime: now}
	s.mu.RLock()
	e := s.clients[d.ConnectionID]
	s.mu.RUnlock()
	if e == nil {
		return result, nil
	}
	status, challenge := e.adminSnapshot(now)
	result.State = status.Auth
	result.Challenge = challenge
	return result, nil
}

// Status returns identity, authentication, transport, recovery and public login
// metadata together without constructing a client, reconnecting or making RPCs.
func (s *AccountService) Status(ctx context.Context, id string) (domains.DeviceStatus, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.DeviceStatus{}, err
	}
	now := time.Now().UTC()
	result := domains.DeviceStatus{Provider: d.Provider, ConnectionStatus: domains.ConnectionStatus{Auth: "auth_required", Transport: "disconnected", Recovery: "degraded"}, ServerTime: now}
	if !s.providerEnabled(d.Provider) {
		result.LastError = "PROVIDER_DISABLED"
	}
	s.mu.RLock()
	e := s.clients[d.ConnectionID]
	s.mu.RUnlock()
	if e != nil {
		result.ConnectionStatus, result.Challenge = e.adminSnapshot(now)
	}
	// Recheck this exact connection after the snapshot, including a binding that
	// completed concurrently. Deletion/reuse must not resolve another alias.
	d, err = s.accountStore.DeviceByConnection(ctx, d.ConnectionID)
	if err != nil {
		return domains.DeviceStatus{}, err
	}
	result.DeviceID, result.InstanceID, result.AccountID = d.ID, d.InstanceToken(), d.AccountID
	return result, nil
}

// ProvisionDevice provisions a connection without contacting the provider. Its
// mandatory key binds the initial request to one immutable device lifetime.
func (s *AccountService) ProvisionDevice(ctx context.Context, request domains.ProvisionDeviceRequest, key string) (domains.Device, bool, error) {
	if err := domains.ValidateProvisioningKey(key); err != nil {
		return domains.Device{}, false, err
	}
	if err := request.Validate(); err != nil {
		return domains.Device{}, false, err
	}
	contract, err := s.provider(request.Provider)
	if err != nil {
		return domains.Device{}, false, err
	}
	if err = validateFilter(contract, request.WebhookFilter); err != nil {
		return domains.Device{}, false, err
	}
	return s.accountStore.ProvisionDevice(ctx, request, key)
}

func (s *AccountService) CreateDevice(ctx context.Context, id string, provider domains.Provider) (domains.Device, error) {
	if strings.TrimSpace(id) == "" {
		return domains.Device{}, domains.E("INVALID_DEVICE_ID", "device id is required", 400)
	}
	if len(id) > 128 || strings.TrimSpace(id) != id || strings.ContainsAny(id, "/\\\x00\r\n") {
		return domains.Device{}, domains.E("INVALID_DEVICE_ID", "device id must be 1–128 characters without path separators or surrounding whitespace", 400)
	}
	if _, err := s.provider(provider); err != nil {
		return domains.Device{}, err
	}
	return s.accountStore.CreateDevice(ctx, id, provider)
}

func (s *AccountService) ListDevices(ctx context.Context) ([]domains.Device, error) {
	return s.accountStore.ListDevices(ctx)
}

func (s *AccountService) GetDevice(ctx context.Context, id string) (domains.Device, error) {
	return s.ResolveDevice(ctx, id)
}

func (s *AccountService) DeleteDevice(ctx context.Context, id string) error {
	device, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return err
	}
	if !s.providerEnabled(device.Provider) {
		return s.accountStore.DeleteDevice(ctx, device.ConnectionID)
	}
	e, err := s.entry(device)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.desired = false
	e.closed = true
	e.setPersistenceActive(false)
	e.clearAuthMetadata()
	if err := s.accountStore.DeleteDevice(ctx, device.ConnectionID); err != nil {
		e.closed = false
		return err
	}
	// Deleting a local connection does not claim that the remote session was revoked.
	_ = e.client.Disconnect(ctx)
	s.mu.Lock()
	delete(s.clients, device.ConnectionID)
	s.mu.Unlock()
	return nil
}

func (s *AccountService) StartAuth(ctx context.Context, id, phone string) (domains.Challenge, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.Challenge{}, err
	}
	if strings.TrimSpace(phone) == "" {
		return domains.Challenge{}, domains.E("INVALID_PHONE", "phone is required", 400)
	}
	e, err := s.entry(d)
	if err != nil {
		return domains.Challenge{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return domains.Challenge{}, domains.E("DEVICE_NOT_FOUND", "device no longer exists", 404)
	}
	session, err := s.accountStore.LoadSession(ctx, d.ConnectionID)
	if err != nil {
		return domains.Challenge{}, err
	}
	if session != nil {
		return domains.Challenge{}, domains.E("ALREADY_AUTHENTICATED", "log out before starting a new authentication challenge", 409)
	}
	e.authMetaMu.Lock()
	now := time.Now().UTC()
	if now.Before(e.resendAvailableAt) {
		wait := int64((time.Until(e.resendAvailableAt) + time.Second - 1) / time.Second)
		e.authMetaMu.Unlock()
		return domains.Challenge{}, &domains.Error{Code: "AUTH_RESEND_TOO_SOON", Message: "wait before requesting another authentication code", HTTP: 429, RetryAfterSeconds: wait}
	}
	// Clear the previous public challenge before the replacement RPC. A failed
	// replacement cannot resurrect a challenge whose provider cookies were reset.
	e.challenge = nil
	e.authMetaMu.Unlock()
	e.desired = false
	challenge, err := e.client.StartAuth(ctx, phone)
	if err != nil {
		return domains.Challenge{}, safeError(err)
	}
	now = time.Now().UTC()
	if challenge.ID == "" {
		return domains.Challenge{}, domains.E("INVALID_PROVIDER_CHALLENGE", "provider did not return an authentication challenge", 502)
	}
	if challenge.ExpiresAt.IsZero() || challenge.ExpiresAt.After(now.Add(10*time.Minute)) {
		challenge.ExpiresAt = now.Add(10 * time.Minute)
	}
	resendAt := now
	if challenge.ResendAfterSeconds != nil {
		seconds := *challenge.ResendAfterSeconds
		if seconds < 0 || seconds > 86400 {
			return domains.Challenge{}, domains.E("INVALID_PROVIDER_CHALLENGE", "provider returned an invalid resend cooldown", 502)
		}
		resendAt = now.Add(time.Duration(seconds) * time.Second)
	}
	e.authMetaMu.Lock()
	e.resendAvailableAt = resendAt
	e.challenge = &domains.PublicChallenge{ID: challenge.ID, ExpiresAt: challenge.ExpiresAt, ResendAvailableAt: resendAt, SentCodeType: challenge.SentCodeType, NextSendCodeType: challenge.NextSendCodeType, AvailableSendCodeTypes: append([]int32{}, challenge.AvailableSendCodeTypes...), Delivery: challenge.Delivery, NextDelivery: challenge.NextDelivery, AvailableDeliveries: append([]string{}, challenge.AvailableDeliveries...), MaskedPhone: maskedPhone(phone)}
	e.authMetaMu.Unlock()
	return challenge, nil
}

func (s *AccountService) SubmitCode(ctx context.Context, id, challenge, code string) (domains.ConnectionStatus, error) {
	return s.submitAuth(ctx, id, challenge, code, false)
}

func (s *AccountService) SubmitPassword(ctx context.Context, id, challenge, password string) (domains.ConnectionStatus, error) {
	return s.submitAuth(ctx, id, challenge, password, true)
}

func (s *AccountService) submitAuth(ctx context.Context, id, challenge, value string, password bool) (domains.ConnectionStatus, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.ConnectionStatus{}, err
	}
	if challenge == "" || value == "" {
		return domains.ConnectionStatus{}, domains.E("INVALID_AUTH", "challenge and authentication value are required", 400)
	}
	e, err := s.entry(d)
	if err != nil {
		return domains.ConnectionStatus{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return domains.ConnectionStatus{}, domains.E("DEVICE_NOT_FOUND", "device no longer exists", 404)
	}
	public := e.publicChallenge(time.Now())
	if public == nil || public.ID != challenge {
		return e.adminStatus(time.Now()), domains.E("CHALLENGE_EXPIRED", "authentication challenge is absent or expired", 400)
	}
	var session *domains.Session
	if password {
		session, err = e.client.SubmitPassword(ctx, challenge, value)
	} else {
		session, err = e.client.SubmitCode(ctx, challenge, value)
	}
	if err != nil {
		var authErr *domains.Error
		if errors.As(err, &authErr) && authErr.Code == "CHALLENGE_EXPIRED" {
			e.authMetaMu.Lock()
			e.challenge = nil
			e.authMetaMu.Unlock()
		}
		return e.adminStatus(time.Now()), safeError(err)
	}
	if session == nil {
		if source, ok := e.client.(domains.AuthChallengeSource); ok {
			e.refreshChallenge(source.CurrentChallenge())
		}
		return cleanStatus(e.client.Status()), nil
	}
	e.clearAuthMetadata()
	contract, contractErr := s.provider(d.Provider)
	if contractErr != nil {
		return cleanStatus(e.client.Status()), contractErr
	}
	if err = contract.ValidateSession(session); err != nil {
		return cleanStatus(e.client.Status()), err
	}
	if err = s.accountStore.SaveSession(ctx, d.ConnectionID, session); err != nil {
		_ = e.client.Disconnect(ctx)
		e.desired = false
		e.setClient(s.factory(d))
		return cleanStatus(e.client.Status()), err
	}
	e.desired = true
	e.failures = 0
	e.nextConnect = time.Time{}
	e.setPersistenceActive(true)
	err = s.connect(ctx, d, e.client, session)
	if err != nil {
		e.failures++
		e.nextConnect = time.Now().Add(reconnectDelay(e.failures))
	}
	return cleanStatus(e.client.Status()), safeError(err)
}

func (s *AccountService) Reconnect(ctx context.Context, id string) error {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return err
	}
	e, err := s.entry(d)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return domains.E("DEVICE_NOT_FOUND", "device no longer exists", 404)
	}
	session, err := s.stopForReconnect(ctx, d.ConnectionID, e)
	if err != nil {
		return err
	}
	if session == nil {
		return domains.E("AUTH_REQUIRED", "connect the account first", 409)
	}
	e.desired = true
	e.setPersistenceActive(true)
	err = s.connect(ctx, d, e.client, session)
	if err != nil {
		e.failures++
		e.nextConnect = time.Now().Add(reconnectDelay(e.failures))
	} else {
		e.failures = 0
		e.nextConnect = time.Time{}
	}
	return safeError(err)
}

func (s *AccountService) Logout(ctx context.Context, id string) error {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return err
	}
	if !s.providerEnabled(d.Provider) {
		if err := s.accountStore.LogoutConnection(ctx, d.ConnectionID); err != nil {
			return err
		}
		return domains.E("REMOTE_LOGOUT_UNCONFIRMED", "local session cleared; remote logout could not be confirmed", 502)
	}
	e, err := s.entry(d)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.desired = false
	e.setPersistenceActive(false)
	e.clearAuthMetadata()
	remoteErr := e.client.Logout(ctx)
	_ = e.client.Disconnect(ctx)
	clearCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err = s.accountStore.LogoutConnection(clearCtx, d.ConnectionID); err != nil {
		return err
	}
	e.setClient(s.factory(d)) // discard tokens and pending challenges retained after a failed remote revoke
	if remoteErr != nil {
		return domains.E("REMOTE_LOGOUT_UNCONFIRMED", "local session cleared; remote logout could not be confirmed", 502)
	}
	return nil
}
