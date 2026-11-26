package balemeow

import (
	"context"
	"testing"

	"github.com/coder/websocket"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestTextSendDiagnosticBaleRejection(t *testing.T) {
	for _, tt := range []struct {
		name                                  string
		hash                                  int64
		message, field, wantReason, wantField string
	}{
		{"absent", 0, "user supplied text with private ID", "private-test-value", "unclassified", ""},
		{"present", 123, "INVALID_ARGUMENT", "peer.access_hash", "INVALID_ARGUMENT", "peer.access_hash"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var fake *fakeWS
			fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
				if m.Request == nil {
					return
				}
				require.Equal(t, "SendMessage", m.Request.Method)
				var req wire.SendMessageRequest
				require.NoError(t, proto.Unmarshal(m.Request.Payload, &req))
				require.Equal(t, tt.hash, req.GetPeer().GetAccessHash())
				fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: m.Request.Index, Error: &wire.Status{
					Code: 3, Message: tt.message, Details: &wire.Metadata{Items: []*wire.MetadataItem{{Key: "field", Value: &wire.MetadataValue{StringValue: tt.field}}}},
				}}})
			})
			c := fake.client()
			connectTest(t, c, acceptingSink)
			if tt.hash != 0 {
				c.rememberRef("user", &wire.PeerRef{Id: 77, AccessHash: tt.hash})
			}
			d := &domains.TextSendDiagnostic{}
			_, err := c.Send(domains.WithTextSendDiagnostic(context.Background(), d), domains.SendRequest{
				Peer: domains.Peer{Type: "user", ID: "77"}, Text: "private test content", RequestID: "123"})
			var de *domains.Error
			require.ErrorAs(t, err, &de)
			require.Equal(t, "PROVIDER_INVALID_ARGUMENT", de.Code)
			require.Equal(t, domains.TextSendDiagnostic{Stage: "provider_error", ProviderCode: 3, Reason: tt.wantReason, PeerHash: tt.name, RejectedField: tt.wantField}, *d)
		})
	}
}
