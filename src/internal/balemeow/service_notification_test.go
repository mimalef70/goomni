package balemeow

import (
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"github.com/stretchr/testify/require"
)

func TestServiceNotificationLiteralWireAndWebhook(t *testing.T) {
	// Independently specified from the official Web encoder: Message field 11,
	// ServiceMessage text=1/ext=2, contact-registered extension=9, uid=1.
	raw, err := hex.DecodeString("5a0e0a066a6f696e656412044a02082a")
	require.NoError(t, err)
	message := &wire.Message{}
	require.NoError(t, decode(raw, message))
	for name, wrapped := range map[string]*wire.Message{
		"direct":            message,
		"template":          {Template: &wire.TemplateMessage{Message: message}},
		"template_response": {TemplateResponse: &wire.TemplateResponse{Message: message}},
	} {
		t.Run(name, func(t *testing.T) {
			event := receivedMessage(t, wrapped, nil)
			require.Equal(t, "service", event.Message.Kind)
			require.Equal(t, "joined", event.Message.Body)
			require.Nil(t, event.Media)
			require.Nil(t, event.Message.Media)
			body, err := json.Marshal(event)
			require.NoError(t, err)
			var envelope map[string]any
			require.NoError(t, json.Unmarshal(body, &envelope))
			require.Equal(t, "service", envelope["payload"].(map[string]any)["kind"])
		})
	}
	require.JSONEq(t, `{"kind":"service","message":"joined","actions":[{"type":"contact_registered","user_id":"42"}]}`, string(messagePayload(message)))
	text := receivedMessage(t, &wire.Message{Text: &wire.TextMessage{Text: "joined"}}, &wire.QuotedMessage{SenderUserId: 42, Date: 100, MessageId: &wire.Int64Value{Value: 1}, Message: message})
	require.Equal(t, "text", text.Message.Kind, "quoted service notices must not classify the actual user message")
	require.Equal(t, "joined", text.Message.Body)
}
