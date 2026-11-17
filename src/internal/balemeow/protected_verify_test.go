package balemeow

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

// Synthetic SMS code only. 135790 fits the int32 wire type.
const (
	syntheticSMSCode = "135790"
	syntheticSMSInt  = int32(135790)
)

type verifyFixture struct {
	sends, validates atomic.Int32
	sendCode         int32
	validateCode     int32
	otpValid         bool
	silentSend       bool
	silentValidate   bool
	// seededToken, when set, makes the first CheckPassword fail with status 7 and
	// a later CheckPassword succeed once the session has been authorized.
	gatedCheck bool
	authorized atomic.Bool
	checks     atomic.Int32
	fetches    atomic.Int32
}

func newVerifyClient(t *testing.T, f *verifyFixture) *Client {
	t.Helper()
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		r := m.Request
		if r == nil {
			if m.Ping != nil {
				fake.send(ws, &wire.ServerMessage{Pong: m.Ping})
			}
			return
		}
		reply := &wire.Response{Index: r.Index}
		freeForm := "provider text " + syntheticSMSCode + " " + syntheticPIN
		switch r.Service + "/" + r.Method {
		case "bale.ramz.v1.Ramz/SendOTP":
			f.sends.Add(1)
			q := &wire.Empty{}
			if decode(r.Payload, q) != nil || len(r.Payload) != 0 {
				t.Errorf("SendOTP must carry an empty request")
			}
			if metadataValue(r.Metadata, "bale_ramz_token") != "" || metadataValue(r.Metadata, "auth-jwt") == "" {
				t.Errorf("SendOTP must use the account session only")
			}
			if f.silentSend {
				return
			}
			if f.sendCode != 0 {
				reply.Error = &wire.Status{Code: f.sendCode, Message: freeForm}
			} else {
				reply.Payload = marshal(t, &wire.Empty{})
			}
		case "bale.ramz.v1.Ramz/ValidateOTP":
			f.validates.Add(1)
			q := &wire.RamzValidateOTPRequest{}
			if decode(r.Payload, q) != nil || q.Otp != syntheticSMSInt || q.ServicesType != 1 {
				t.Errorf("invalid ValidateOTP request: %+v", q)
			}
			if f.silentValidate {
				return
			}
			if f.validateCode != 0 {
				reply.Error = &wire.Status{Code: f.validateCode, Message: freeForm}
			} else {
				f.authorized.Store(true)
				reply.Payload = marshal(t, &wire.RamzValidateOTPResponse{OtpValid: f.otpValid})
			}
		case "bale.ramz.v1.Ramz/CheckPassword":
			f.checks.Add(1)
			if f.gatedCheck && !f.authorized.Load() {
				reply.Error = &wire.Status{Code: 7, Message: freeForm}
			} else {
				reply.Payload = marshal(t, &wire.RamzCheckPasswordResponse{Token: syntheticRamzToken})
			}
		case "bale.messaging.v2.Messaging/FetchProtectedMessage":
			f.fetches.Add(1)
			reply.Payload = marshal(t, &wire.FetchProtectedMessageResponse{History: protectedHistory(&wire.Message{Text: &wire.TextMessage{Text: syntheticOTPText}})})
		default:
			t.Errorf("unexpected provider call %s/%s", r.Service, r.Method)
			return
		}
		fake.send(ws, &wire.ServerMessage{Response: reply})
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	return c
}

func TestProtectedVerifyLiteralValidateRequest(t *testing.T) {
	// Independent of the generated encoder: otp=1 varint, services_type=2 varint.
	want := protowire.AppendVarint(protowire.AppendTag(nil, 1, protowire.VarintType), uint64(syntheticSMSInt))
	want = protowire.AppendVarint(protowire.AppendTag(want, 2, protowire.VarintType), 1)
	require.Equal(t, want, marshal(t, &wire.RamzValidateOTPRequest{Otp: syntheticSMSInt, ServicesType: 1}))
	// SendOTP request is empty.
	require.Empty(t, marshal(t, &wire.Empty{}))
}

func TestProtectedVerifyStartSendsOTP(t *testing.T) {
	f := &verifyFixture{}
	c := newVerifyClient(t, f)
	out, err := c.Call(context.Background(), "message.protected.verify.start", nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"verification_sent":true}`, string(out))
	require.EqualValues(t, 1, f.sends.Load())

	// A non-empty body is rejected before any network call.
	f2 := &verifyFixture{}
	c2 := newVerifyClient(t, f2)
	_, err = c2.Call(context.Background(), "message.protected.verify.start", json.RawMessage(`{"peer":{"type":"user","id":"42"}}`))
	require.Equal(t, "INVALID_REQUEST", codeOf(err))
	require.Zero(t, f2.sends.Load())
}

func TestProtectedVerifySubmitSucceeds(t *testing.T) {
	f := &verifyFixture{otpValid: true}
	c := newVerifyClient(t, f)
	out, err := c.Call(context.Background(), "message.protected.verify.submit", json.RawMessage(`{"verification_code":"`+syntheticSMSCode+`"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"verified":true}`, string(out))
	require.NotContains(t, string(out), syntheticSMSCode)
	require.EqualValues(t, 1, f.validates.Load())
}

func TestProtectedVerifySubmitWrongCodeIsNotRetried(t *testing.T) {
	f := &verifyFixture{otpValid: false}
	c := newVerifyClient(t, f)
	_, err := c.Call(context.Background(), "message.protected.verify.submit", json.RawMessage(`{"verification_code":"`+syntheticSMSCode+`"}`))
	require.Equal(t, "PROTECTED_VERIFICATION_REJECTED", codeOf(err))
	require.EqualValues(t, 1, f.validates.Load(), "a verification code is never retried")
	require.NotEqual(t, "auth_required", c.Status().Auth)
}

func TestProtectedVerifyProviderErrorsMap(t *testing.T) {
	for _, tc := range []struct {
		name     string
		op, body string
		send     int32
		validate int32
		want     string
		http     int
	}{
		{"start-already-sent", "message.protected.verify.start", ``, 6, 0, "PROTECTED_VERIFICATION_RATE_LIMITED", 429},
		{"start-aborted", "message.protected.verify.start", ``, 10, 0, "PROTECTED_VERIFICATION_RATE_LIMITED", 429},
		{"submit-aborted", "message.protected.verify.submit", `{"verification_code":"` + syntheticSMSCode + `"}`, 0, 10, "PROTECTED_VERIFICATION_RATE_LIMITED", 429},
		{"submit-invalid-arg", "message.protected.verify.submit", `{"verification_code":"` + syntheticSMSCode + `"}`, 0, 3, "PROVIDER_INVALID_ARGUMENT", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &verifyFixture{sendCode: tc.send, validateCode: tc.validate}
			c := newVerifyClient(t, f)
			_, err := c.Call(context.Background(), tc.op, json.RawMessage(tc.body))
			var de *domains.Error
			require.True(t, errors.As(err, &de))
			require.Equal(t, tc.want, de.Code)
			require.Equal(t, tc.http, de.HTTP)
			require.False(t, de.Retryable)
			requireNoSecrets(t, err.Error()+de.Message)
			require.NotContains(t, err.Error(), syntheticSMSCode)
		})
	}
}

func TestProtectedVerifyUnauthenticatedRevokesSession(t *testing.T) {
	// SendOTP/ValidateOTP are not PIN checks; a genuine UNAUTHENTICATED reply
	// still revokes the account session.
	for _, tc := range []struct{ op, body string }{
		{"message.protected.verify.start", ``},
		{"message.protected.verify.submit", `{"verification_code":"` + syntheticSMSCode + `"}`},
	} {
		t.Run(tc.op, func(t *testing.T) {
			f := &verifyFixture{sendCode: 16, validateCode: 16}
			c := newVerifyClient(t, f)
			_, err := c.Call(context.Background(), tc.op, json.RawMessage(tc.body))
			require.Equal(t, "AUTH_REQUIRED", codeOf(err))
			eventually(t, func() bool { return c.Status().Auth == "auth_required" })
		})
	}
}

func TestProtectedVerifyTimeoutIsNotRetried(t *testing.T) {
	for _, tc := range []struct {
		name, op, body string
		silentSend     bool
		silentValidate bool
	}{
		{"start", "message.protected.verify.start", ``, true, false},
		{"submit", "message.protected.verify.submit", `{"verification_code":"` + syntheticSMSCode + `"}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &verifyFixture{silentSend: tc.silentSend, silentValidate: tc.silentValidate}
			c := newVerifyClient(t, f)
			_, err := c.Call(context.Background(), tc.op, json.RawMessage(tc.body))
			var de *domains.Error
			require.True(t, errors.As(err, &de))
			require.Equal(t, "PROTECTED_VERIFICATION_INCOMPLETE", de.Code)
			require.False(t, de.Retryable)
			require.False(t, de.Ambiguous)
		})
	}
}

func TestProtectedVerifyRejectsInvalidCodeBeforeNetwork(t *testing.T) {
	f := &verifyFixture{otpValid: true}
	c := newVerifyClient(t, f)
	for _, body := range []string{
		`{"verification_code":"12"}`,
		`{"verification_code":"1234567890"}`,
		`{"verification_code":"۱۳۵۷۹۰"}`,
		`{"verification_code":"13579a"}`,
		`{"verification_code":""}`,
		`{"verification_code":"135790","verification_code":"246801"}`,
	} {
		_, err := c.Call(context.Background(), "message.protected.verify.submit", json.RawMessage(body))
		require.Equal(t, "INVALID_REQUEST", codeOf(err), body)
		require.NotContains(t, err.Error(), "246801")
	}
	require.Zero(t, f.validates.Load())

	offline := New(Options{})
	_, err := offline.Call(context.Background(), "message.protected.verify.start", nil)
	require.Equal(t, "CONNECTION_UNAVAILABLE", codeOf(err))
}

func TestProtectedUnlockAfterVerificationSucceeds(t *testing.T) {
	// End to end: unlock blocks on status 7, verification authorizes the session,
	// and the retried unlock returns the content.
	f := &verifyFixture{gatedCheck: true, otpValid: true}
	c := newVerifyClient(t, f)
	unlock := unlockBody(syntheticPIN)

	_, err := c.Call(context.Background(), "message.protected.unlock", unlock)
	require.Equal(t, "PROTECTED_PIN_VERIFICATION_REQUIRED", codeOf(err))
	require.Zero(t, f.fetches.Load())

	_, err = c.Call(context.Background(), "message.protected.verify.start", nil)
	require.NoError(t, err)
	out, err := c.Call(context.Background(), "message.protected.verify.submit", json.RawMessage(`{"verification_code":"`+syntheticSMSCode+`"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"verified":true}`, string(out))

	out, err = c.Call(context.Background(), "message.protected.unlock", unlock)
	require.NoError(t, err)
	require.Contains(t, string(out), syntheticOTPText)
	require.NotContains(t, string(out), syntheticPIN)
	require.NotContains(t, string(out), syntheticSMSCode)
	require.EqualValues(t, 1, f.fetches.Load())
	require.EqualValues(t, 2, f.checks.Load(), "one blocked check, then one authorized check")
}
