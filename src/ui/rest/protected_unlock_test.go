package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
)

func TestProtectedUnlockPINIsBodyOnlyAndNeverPersisted(t *testing.T) {
	// Synthetic values only.
	const pin, otp = "246810", "synthetic one-time content 000000"
	calls := make(chan json.RawMessage, 4)
	s, svc := setupAPIWithFactory(t, "", func(domains.Device) domains.Client {
		return &testClient{callFn: func(_ context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
			require.Equal(t, "message.protected.unlock", op)
			calls <- raw
			var p map[string]any
			require.NoError(t, json.Unmarshal(raw, &p))
			if p["pin"] != pin {
				return nil, domains.E("PROTECTED_PIN_REJECTED", "provider rejected the PIN; the attempt was not retried", 403)
			}
			return json.RawMessage(`{"locked":false,"content":{"kind":"text","message":"` + otp + `"}}`), nil
		}}
	})
	device, err := svc.CreateDevice(context.Background(), "one", domains.ProviderBale)
	require.NoError(t, err)
	body := func(pin string) string {
		return `{"peer":{"type":"user","id":"42"},"message_id":"9007199254740993","date":"1720000000001","pin":"` + pin + `"}`
	}
	do := func(path, payload string) (int, string) {
		r := httptest.NewRequest("POST", path, bytes.NewBufferString(payload))
		r.SetBasicAuth("test", "password")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Device-Id", "one")
		scopeTestRequest(t, s, r)
		res, err := s.App.Test(r)
		require.NoError(t, err)
		defer res.Body.Close()
		out, err := io.ReadAll(res.Body)
		require.NoError(t, err)
		require.Equal(t, "no-store", res.Header.Get("Cache-Control"))
		return res.StatusCode, string(out)
	}

	for _, path := range []string{"/message/protected/unlock?pin=" + pin, "/operations/message.protected.unlock?pin=" + pin} {
		status, out := do(path, `{"peer":{"type":"user","id":"42"},"message_id":"9007199254740993","date":"1720000000001"}`)
		require.Equal(t, 400, status, out)
		require.Contains(t, out, "INVALID_REQUEST")
		status, out = do(path, body(pin))
		require.Equal(t, 400, status, "a query secret is rejected even when the body has one")
		require.NotContains(t, out, pin)
	}
	require.Empty(t, calls)

	status, out := do("/message/protected/unlock", body(pin))
	require.Equal(t, 200, status, out)
	require.Contains(t, out, otp)
	require.NotContains(t, out, pin)
	require.JSONEq(t, body(pin), string(<-calls))
	status, out = do("/operations/message.protected.unlock", body("135790"))
	require.Equal(t, 403, status)
	require.Contains(t, out, "PROTECTED_PIN_REJECTED")
	require.NotContains(t, out, "135790")
	<-calls

	// Ephemeral calls create no operation journal rows or events.
	device, err = svc.GetDevice(context.Background(), device.ID)
	require.NoError(t, err)
	ops, err := s.testStore().ListOperations(context.Background(), device.ConnectionID, 100, 0)
	require.NoError(t, err)
	require.Empty(t, ops)
	status, events := apiRequest(t, s, "GET", "/events", "one", nil)
	require.Equal(t, 200, status)
	encoded, err := json.Marshal(events)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), pin)
	require.NotContains(t, string(encoded), otp)
}

func TestProtectedVerifySMSCodeIsBodyOnly(t *testing.T) {
	// Synthetic SMS code only.
	const code = "135790"
	calls := make(chan json.RawMessage, 4)
	s, svc := setupAPIWithFactory(t, "", func(domains.Device) domains.Client {
		return &testClient{callFn: func(_ context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
			calls <- raw
			switch op {
			case "message.protected.verify.start":
				return json.RawMessage(`{"verification_sent":true}`), nil
			case "message.protected.verify.submit":
				var p map[string]any
				require.NoError(t, json.Unmarshal(raw, &p))
				if p["verification_code"] != code {
					return nil, domains.E("PROTECTED_VERIFICATION_REJECTED", "provider rejected the verification code; it was not retried", 403)
				}
				return json.RawMessage(`{"verified":true}`), nil
			default:
				return nil, domains.Unsupported(op)
			}
		}}
	})
	device, err := svc.CreateDevice(context.Background(), "one", domains.ProviderBale)
	require.NoError(t, err)
	do := func(method, path, payload string) (int, string) {
		var r *http.Request
		if payload == "" {
			r = httptest.NewRequest(method, path, nil)
		} else {
			r = httptest.NewRequest(method, path, bytes.NewBufferString(payload))
			r.Header.Set("Content-Type", "application/json")
		}
		r.SetBasicAuth("test", "password")
		r.Header.Set("X-Device-Id", "one")
		scopeTestRequest(t, s, r)
		res, err := s.App.Test(r)
		require.NoError(t, err)
		defer res.Body.Close()
		out, err := io.ReadAll(res.Body)
		require.NoError(t, err)
		require.Equal(t, "no-store", res.Header.Get("Cache-Control"))
		return res.StatusCode, string(out)
	}

	status, out := do("POST", "/message/protected/verify/start", "")
	require.Equal(t, 200, status, out)
	require.Contains(t, out, "verification_sent")

	// A code in the URL is rejected, with or without a body.
	for _, path := range []string{"/message/protected/verify/submit?verification_code=" + code, "/operations/message.protected.verify.submit?verification_code=" + code} {
		status, out = do("POST", path, `{"verification_code":"`+code+`"}`)
		require.Equal(t, 400, status)
		require.NotContains(t, out, code)
	}

	status, out = do("POST", "/message/protected/verify/submit", `{"verification_code":"`+code+`"}`)
	require.Equal(t, 200, status, out)
	require.Contains(t, out, "verified")
	status, out = do("POST", "/message/protected/verify/submit", `{"verification_code":"000111"}`)
	require.Equal(t, 403, status)
	require.Contains(t, out, "PROTECTED_VERIFICATION_REJECTED")
	require.NotContains(t, out, "000111")

	// Ephemeral calls leave no operation journal rows.
	device, err = svc.GetDevice(context.Background(), device.ID)
	require.NoError(t, err)
	ops, err := s.testStore().ListOperations(context.Background(), device.ConnectionID, 100, 0)
	require.NoError(t, err)
	require.Empty(t, ops)

	drained := []string{}
	for {
		select {
		case raw := <-calls:
			drained = append(drained, string(raw))
			continue
		default:
		}
		break
	}
	require.Contains(t, strings.Join(drained, "\n"), `"verification_code":"`+code+`"`, "the submit call must receive the code in its body")
}
