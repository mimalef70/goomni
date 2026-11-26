package eitaameow

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
)

func TestTextSendDiagnosticKeepsOriginalResponse(t *testing.T) {
	for _, tt := range []struct {
		name         string
		response     object
		http         int
		persistFails bool
		expected     domains.TextSendDiagnostic
	}{
		{"renewed", object{"_": "eitta_error", "code": 401, "text": "INVALID_LOGIN"}, 200, false,
			domains.TextSendDiagnostic{Stage: "provider_error", HTTPStatus: 200, ProviderCode: 401, Reason: "INVALID_LOGIN", Renewal: "completed"}},
		{"renewal failed", object{"_": "eitaa_updates_expire_token"}, 200, true,
			domains.TextSendDiagnostic{Stage: "provider_error", HTTPStatus: 200, Reason: "eitaa_updates_expire_token", Renewal: "failed"}},
		{"missing proof", object{"_": "updatesTooLong"}, 200, false,
			domains.TextSendDiagnostic{Stage: "verify_send_id", HTTPStatus: 200, Reason: "updatesTooLong"}},
		{"upstream http", nil, 503, false,
			domains.TextSendDiagnostic{Stage: "http_response", HTTPStatus: 503}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var sends, renewals atomic.Int32
			c, _ := nativeFixture(t, func(method string, p object) (object, int) {
				if method == "eitaaRefreshToken" {
					renewals.Add(1)
					return object{"_": "eitaa_updates_token", "date": 100, "expire": 300, "token": "private-test-token"}, 200
				}
				if method != "messages.sendMessage" {
					t.Errorf("unexpected RPC %s", method)
				}
				sends.Add(1)
				return tt.response, tt.http
			})
			c.session.Token, c.session.UserID = "synthetic-old", "42"
			c.SetSessionPersister(func(context.Context, *domains.Session) error {
				if tt.persistFails {
					return errors.New("private persistence detail")
				}
				return nil
			})
			d := &domains.TextSendDiagnostic{}
			_, err := c.Send(domains.WithTextSendDiagnostic(context.Background(), d),
				domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "42"}, Kind: "text", Text: "private test content", RequestID: "77"})
			var de *domains.Error
			require.ErrorAs(t, err, &de)
			require.True(t, de.Ambiguous)
			require.Equal(t, int32(1), sends.Load())
			if tt.expected.Renewal == "" {
				require.Zero(t, renewals.Load())
			} else {
				require.Equal(t, int32(1), renewals.Load())
			}
			require.Equal(t, tt.expected, *d)
		})
	}
}

func TestTextSendDiagnosticClosedEndpoint(t *testing.T) {
	c, srv := nativeFixture(t, func(string, object) (object, int) { t.Error("unexpected request"); return nil, 500 })
	srv.Close()
	c.session.Token, c.session.UserID = "synthetic", "42"
	d := &domains.TextSendDiagnostic{}
	_, err := c.Send(domains.WithTextSendDiagnostic(context.Background(), d), domains.SendRequest{Peer: domains.Peer{Type: "user", ID: "42"}, Text: "private test content", RequestID: "77"})
	require.Error(t, err)
	require.Equal(t, domains.TextSendDiagnostic{Stage: "http_request", Reason: "dial"}, *d)
}
