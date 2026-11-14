package rubikameow

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServiceNotificationNativeObjectAndWebhook(t *testing.T) {
	client, err := New(Config{})
	require.NoError(t, err)
	// Native object layout from the official Web renderer. No signup subtype is
	// invented: every Event is a service message, including unknown subtypes.
	update, err := jsonObject([]byte(`{"action":"New","message_id":"42","timestamp":"100","message":{"time":100,"type":"Event","text":"synthetic notice","author_object_guid":"u0peer","event_data":{"type":"JoinedGroupByLink","access_hash":"private"}}}`))
	require.NoError(t, err)
	for _, action := range []string{"New", "Edit"} {
		update["action"] = action
		event, err := client.projectMessage("u0peer", update)
		require.NoError(t, err)
		require.Equal(t, "service", event.Message.Kind)
		require.True(t, event.Message.Supported)
		require.Nil(t, event.Media)
		body, err := json.Marshal(event)
		require.NoError(t, err)
		var envelope map[string]any
		require.NoError(t, json.Unmarshal(body, &envelope))
		require.Equal(t, "service", envelope["payload"].(map[string]any)["kind"])
		require.Equal(t, "JoinedGroupByLink", envelope["content"].(map[string]any)["provider_action"])
		require.NotContains(t, string(body), "private")
	}
	message := asObject(update["message"])
	message["event_data"] = object{"type": "unreviewedAction", "private": "private"}
	// Event records must not acquire media from unrelated/unknown fields.
	message["file_inline"] = object{"malformed": true}
	event, err := client.projectMessage("u0peer", update)
	require.NoError(t, err)
	require.Equal(t, "service", event.Message.Kind)
	require.False(t, event.Message.Supported)
	require.Nil(t, event.Media)
	require.Nil(t, event.Message.Media)
	require.NotContains(t, string(event.Payload), "private")
	delete(message, "file_inline")
	message["type"] = "Text"
	message["text"] = "مخاطب به پیام‌رسان پیوست"
	event, err = client.projectMessage("u0peer", update)
	require.NoError(t, err)
	require.Equal(t, "text", event.Message.Kind)
	require.True(t, event.Message.Supported)
}
