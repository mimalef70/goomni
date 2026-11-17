package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
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
