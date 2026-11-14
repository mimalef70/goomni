package eitaameow

import (
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServiceNotificationLiteralWireAndWebhook(t *testing.T) {
	// Literal TL constructors and field order, independent of our TL encoder:
	// messageService#2b085862, peerUser#59511722, contact signup#f3f25f76.
	raw, err := hex.DecodeString("6258082b000100002a00000022175159070000000000000022175159070000000000000064000000765ff2f3")
	require.NoError(t, err)
	codec, err := bundledCodec()
	require.NoError(t, err)
	decoded, err := codec.decode(raw, "Message")
	require.NoError(t, err)
	client, err := New(Config{})
	require.NoError(t, err)
	for _, kind := range []string{"message", "message.edited"} {
		event, err := client.projectMessage(asObject(decoded), kind)
		require.NoError(t, err)
		require.Equal(t, "service", event.Message.Kind)
		require.True(t, event.Message.Supported)
		require.Nil(t, event.Media)
		body, err := json.Marshal(event)
		require.NoError(t, err)
		var envelope map[string]any
		require.NoError(t, json.Unmarshal(body, &envelope))
		require.Equal(t, "service", envelope["payload"].(map[string]any)["kind"])
		content := envelope["content"].(map[string]any)
		require.Equal(t, "messageActionContactSignUp", content["provider_action"])
		require.Equal(t, []any{map[string]any{"type": "contact_registered"}}, content["actions"])
	}
	// Unknown service actions remain classified, without projecting private data.
	message := asObject(decoded)
	message["action"] = object{"_": "unreviewedAction", "access_hash": "private"}
	event, err := client.projectMessage(message, "message")
	require.NoError(t, err)
	require.Equal(t, "service", event.Message.Kind)
	require.False(t, event.Message.Supported)
	require.NotContains(t, string(event.Payload), "private")
	// Identical words in a normal message must stay ordinary text.
	message["_"] = "message"
	message["message"] = "مخاطب به پیام‌رسان پیوست"
	event, err = client.projectMessage(message, "message")
	require.NoError(t, err)
	require.Equal(t, "text", event.Message.Kind)
	require.True(t, event.Message.Supported)
}
