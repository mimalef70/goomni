package rubikameow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

// Synthetic copy of the RubinoStory reply layout observed live on 2026-11-12:
// the reply has no message "text"; the typed text is rubino_story_data.reply_text.
const syntheticStoryReply = `{"message_id":"1000000000000001","time":"1794500000","is_edited":false,"type":"RubinoStory","author_type":"User","author_object_guid":"u0synthetic","rubino_story_data":{"story_id":"0123456789abcdef01234567","story_profile_id":"89abcdef0123456789abcdef","type":"Reply","reply_text":"synthetic story reply","track_id":"Messenger"}}`

func TestStoryReplyUsesReplyText(t *testing.T) {
	var record object
	if err := json.Unmarshal([]byte(syntheticStoryReply), &record); err != nil {
		t.Fatal(err)
	}
	msg := &domains.Message{}
	out, handled, err := projectExtendedMessage(record, msg)
	b, _ := json.Marshal(out)
	if err != nil || !handled || msg.Kind != "story_reply" || !msg.Supported || msg.Body != "synthetic story reply" {
		t.Fatalf("story reply projection %s %#v %v", b, msg, err)
	}
	for _, want := range []string{`"story_id":"0123456789abcdef01234567"`, `"story_profile_id":"89abcdef0123456789abcdef"`, `"type":"Reply"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing %s in %s", want, b)
		}
	}
	if strings.Contains(string(b), "track_id") || strings.Contains(string(b), "reply_text") {
		t.Fatalf("unreviewed field projected: %s", b)
	}
}

func TestStoryReplyKeepsExistingCaption(t *testing.T) {
	msg := &domains.Message{Body: "caption"}
	_, _, _ = projectExtendedMessage(object{"rubino_story_data": object{"type": "Reply", "reply_text": "reply"}}, msg)
	if msg.Body != "caption" || msg.Kind != "story_reply" {
		t.Fatalf("caption overwritten %#v", msg)
	}
}

func TestNonReplyStoryStaysUnsupported(t *testing.T) {
	msg := &domains.Message{}
	_, handled, err := projectExtendedMessage(object{"rubino_story_data": object{"story_id": "0123456789abcdef01234567", "type": "Share"}}, msg)
	if err != nil || !handled || msg.Supported || msg.Kind != "story" || msg.Body != "" {
		t.Fatalf("shared story %#v %v", msg, err)
	}
}

func TestMalformedStoryIsExplicitNotFatal(t *testing.T) {
	for _, story := range []any{
		"invalid",
		object{"story_id": 42},
		object{"story_id": " padded-id"},
		object{"type": "Reply"},
		object{"type": "Reply", "reply_text": 7},
		object{"type": "Reply", "reply_text": strings.Repeat("x", maxStoryReplyText+1)},
		object{"type": strings.Repeat("x", 65)},
	} {
		msg := &domains.Message{}
		out, handled, err := projectExtendedMessage(object{"rubino_story_data": story}, msg)
		b, _ := json.Marshal(out)
		if err != nil || !handled || msg.Supported || msg.Body != "" || !strings.Contains(string(b), `"valid":false`) {
			t.Fatalf("malformed story %#v -> %s %#v %v", story, b, msg, err)
		}
	}
}

func TestStoryReplyEventCarriesBodyAndText(t *testing.T) {
	c := &Client{session: privateSession{UserID: "u0account"}}
	var message object
	if err := json.Unmarshal([]byte(syntheticStoryReply), &message); err != nil {
		t.Fatal(err)
	}
	event, err := c.projectMessage("u0synthetic", object{"action": "New", "object_guid": "u0synthetic", "message_id": "1000000000000001", "message": message})
	if err != nil || event.Message == nil || event.Message.Body != "synthetic story reply" || event.Direction != "incoming" {
		t.Fatalf("event %#v %v", event, err)
	}
	var payload map[string]any
	_ = json.Unmarshal(event.Payload, &payload)
	if payload["text"] != "synthetic story reply" || payload["kind"] != "story_reply" || payload["supported"] != true || payload["rubino_story"] == nil {
		t.Fatalf("payload %s", event.Payload)
	}
}
