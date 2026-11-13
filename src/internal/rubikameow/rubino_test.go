package rubikameow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

// fakeRubino serves synthetic Rubino responses keyed by method name.
type fakeRubino struct {
	mu       sync.Mutex
	srv      *httptest.Server
	handlers map[string]func(data map[string]any) any
	calls    []map[string]any
	image    []byte
}

func newFakeRubino(t *testing.T) *fakeRubino {
	f := &fakeRubino{handlers: map[string]func(map[string]any) any{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/picture/") {
			w.Header().Set("Content-Type", "image/jpg")
			w.Header().Set("Content-Length", "4")
			if r.Method == http.MethodGet {
				w.Write(f.image)
			}
			return
		}
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		if json.Unmarshal(raw, &body) != nil {
			http.Error(w, "bad", 400)
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, body)
		handler := f.handlers[body["method"].(string)]
		f.mu.Unlock()
		if handler == nil {
			json.NewEncoder(w).Encode(map[string]any{"status": "ERROR_ACTION", "status_det": "INVALID_INPUT"})
			return
		}
		data, _ := body["data"].(map[string]any)
		json.NewEncoder(w).Encode(map[string]any{"status": "OK", "status_det": "OK", "data": handler(data)})
	}))
	f.image = []byte{0xff, 0xd8, 0xff, 0xd9}
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRubino) client(t *testing.T, checkpoints map[string]string) *Client {
	c, err := New(Config{RubinoEndpoint: f.srv.URL + "/", APIEndpoint: f.srv.URL, HTTPClient: f.srv.Client(), ConnectionID: "fixture", MaxMediaBytes: 1 << 20,
		LoadCheckpoint: func(_ context.Context, scope string) (string, error) { return checkpoints[scope], nil }})
	if err != nil {
		t.Fatal(err)
	}
	c.session = privateSession{Auth: "rawsyntheticauth", UserID: "u0account"}
	return c
}

func (f *fakeRubino) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, call := range f.calls {
		out = append(out, call["method"].(string))
	}
	return out
}

func profileList(map[string]any) any {
	return map[string]any{"profiles": []any{map[string]any{"id": "prof1", "username": "shop", "is_default": true, "full_photo_url": "https://private.example/photo"}}}
}

func TestRubinoSendsRawAuthInPlainEnvelope(t *testing.T) {
	f := newFakeRubino(t)
	f.handlers["getProfileList"] = profileList
	c := f.client(t, nil)
	out, handled, err := c.rubinoCall(context.Background(), "rubino.profiles", json.RawMessage(`{}`), "")
	if err != nil || !handled {
		t.Fatal(err)
	}
	call := f.calls[0]
	if call["auth"] != "rawsyntheticauth" || call["api_version"] != "0" || call["data_enc"] != nil {
		t.Fatalf("envelope %#v", call)
	}
	if !strings.Contains(string(out), `"username":"shop"`) || strings.Contains(string(out), "photo") {
		t.Fatalf("profiles projection %s", out)
	}
}

func TestRubinoCommentReplyUsesRequestIDAsRnd(t *testing.T) {
	f := newFakeRubino(t)
	f.handlers["getProfileList"] = profileList
	var sent map[string]any
	f.handlers["addReplyComment"] = func(d map[string]any) any {
		sent = d
		return map[string]any{"comment": map[string]any{"id": "c2", "content": d["content"], "parent_id": d["comment_id"]}}
	}
	c := f.client(t, nil)
	payload := json.RawMessage(`{"post_id":"post1","post_profile_id":"prof1","comment_id":"c1","text":"thanks"}`)
	if _, _, err := c.rubinoCall(context.Background(), "rubino.comment.reply", payload, ""); err == nil {
		t.Fatal("mutation without request ID accepted")
	}
	out, _, err := c.rubinoCall(context.Background(), "rubino.comment.reply", payload, "4242")
	if err != nil || sent["rnd"] != float64(4242) || sent["comment_id"] != "c1" || sent["profile_id"] != "prof1" || !strings.Contains(string(out), `"parent_id":"c1"`) {
		t.Fatalf("reply %s %#v %v", out, sent, err)
	}
}

func TestRubinoProfileChatPrefersChatLink(t *testing.T) {
	f := newFakeRubino(t)
	f.handlers["getProfileInfo"] = func(d map[string]any) any {
		return map[string]any{"profile": map[string]any{"id": d["target_profile_id"], "username": "customer", "is_message_allowed": true, "chat_link": map[string]any{"open_chat_data": map[string]any{"object_guid": "u0customer", "object_type": "User"}}}}
	}
	c := f.client(t, nil)
	peer, err := c.rubinoProfileChat(context.Background(), "prof1", "prof2")
	if err != nil || peer != (domains.Peer{Type: "user", ID: "u0customer"}) {
		t.Fatalf("peer %#v %v", peer, err)
	}
	f.handlers["getProfileInfo"] = func(d map[string]any) any {
		return map[string]any{"profile": map[string]any{"id": d["target_profile_id"], "username": "customer", "is_message_allowed": false}}
	}
	var de *domains.Error
	if _, err := c.rubinoProfileChat(context.Background(), "prof1", "prof2"); !asDomainError(err, &de) || de.Code != "RUBINO_MESSAGES_NOT_ALLOWED" {
		t.Fatalf("closed profile %v", err)
	}
}

func TestRubinoEventKinds(t *testing.T) {
	for model, want := range map[string]string{"CommentPost": "rubino.comment", "TagComment": "rubino.mention", "LikePost": "rubino.activity", "LikeComment": "rubino.activity", "Follow": "rubino.activity"} {
		if got := rubinoEventKind(model); got != want {
			t.Fatalf("%s -> %s", model, got)
		}
	}
	if !rubinoIDNewer("17945098586063956191882", "17945094218233801566174") || rubinoIDNewer("9", "10") {
		t.Fatal("ID ordering")
	}
}

func TestNormalizeRubinoRejectsUnsafeIDs(t *testing.T) {
	for _, raw := range []string{`{"post_id":"../x"}`, `{"story_ids":["a","a"]}`, `{"comment_id":"a b"}`} {
		if normalizeRubino("rubino.comments", json.RawMessage(raw)) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if normalizeRubino("rubino.comment.add", json.RawMessage(`{"text":"   "}`)) == nil {
		t.Fatal("blank comment accepted")
	}
}

func TestRubinoPollBaselinesThenEmitsThreadReply(t *testing.T) {
	f := newFakeRubino(t)
	f.handlers["getProfileList"] = profileList
	records := []any{map[string]any{"id": "100", "model": "CommentPost", "comment_id": "old", "post_id": "post1", "post_profile_id": "prof1"}}
	f.handlers["getNewEvents"] = func(map[string]any) any { return map[string]any{"records": records} }
	f.handlers["getComments"] = func(map[string]any) any {
		return map[string]any{"comments": []any{map[string]any{"id": "top", "content": "price?", "reply_count": 1}}}
	}
	f.handlers["getCommentReplies"] = func(d map[string]any) any {
		return map[string]any{"comments": []any{map[string]any{"id": "c9", "content": "thanks", "profile_username": "customer"}}}
	}
	f.handlers["getProfileInfo"] = func(d map[string]any) any {
		return map[string]any{"profile": map[string]any{"id": d["target_profile_id"], "username": "customer", "chat_link": map[string]any{"open_chat_data": map[string]any{"object_guid": "u0customer", "object_type": "User"}}}}
	}
	checkpoints := map[string]string{}
	var batches []domains.EventBatch
	sink := func(_ context.Context, b domains.EventBatch) error {
		batches = append(batches, b)
		for _, t := range b.Checkpoints {
			checkpoints[t.Scope] = t.Next
		}
		return nil
	}
	c := f.client(t, checkpoints)
	if err := c.rubinoPollOnce(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if len(batches) != 1 || len(batches[0].Events) != 0 || !strings.Contains(checkpoints[rubinoScope], `"last_id":"100"`) {
		t.Fatalf("baseline %#v %v", batches, checkpoints)
	}
	records = append([]any{
		map[string]any{"id": "102", "model": "TagComment", "comment_id": "c9", "post_id": "post1", "post_profile_id": "prof1", "owners": []any{map[string]any{"id": "prof2", "username": "customer"}}},
		map[string]any{"id": "101", "model": "CommentPost", "comment_id": "c9", "post_id": "post1", "post_profile_id": "prof1", "create_date": 1794500000, "owners": []any{map[string]any{"id": "prof2", "username": "customer"}}},
	}, records...)
	if err := c.rubinoPollOnce(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	events := batches[1].Events
	if len(events) != 2 || events[0].Type != "rubino.comment" || events[1].Type != "rubino.mention" || events[0].Peer.ID != "u0customer" {
		t.Fatalf("events %#v", events)
	}
	var payload map[string]any
	_ = json.Unmarshal(events[0].Payload, &payload)
	comment, _ := payload["comment"].(map[string]any)
	if payload["content_status"] != "available" || comment["content"] != "thanks" || comment["parent_id"] != "top" {
		t.Fatalf("payload %s", events[0].Payload)
	}
	if !strings.Contains(checkpoints[rubinoScope], `"last_id":"102"`) || events[0].ID == events[1].ID {
		t.Fatalf("checkpoint %v", checkpoints)
	}
	// An unchanged feed writes nothing.
	before := len(batches)
	if err := c.rubinoPollOnce(context.Background(), sink); err != nil || len(batches) != before {
		t.Fatalf("idle poll wrote %d batches %v", len(batches)-before, err)
	}
}

func TestStoryReplyGetsStoryImage(t *testing.T) {
	f := newFakeRubino(t)
	f.handlers["getProfileList"] = profileList
	f.handlers["getStory"] = func(d map[string]any) any {
		return map[string]any{"stories": []any{map[string]any{"id": "story1", "story_type": "Video", "full_file_url": f.srv.URL + "/video/v", "full_snapshot_url": f.srv.URL + "/picture/p"}}}
	}
	c := f.client(t, nil)
	var message object
	if err := json.Unmarshal([]byte(`{"message_id":"1000000000000001","time":"1794500000","type":"RubinoStory","author_object_guid":"u0customer","rubino_story_data":{"story_id":"story1","story_profile_id":"prof1","type":"Reply","reply_text":"hi"}}`), &message); err != nil {
		t.Fatal(err)
	}
	event, err := c.projectMessage("u0customer", object{"action": "New", "object_guid": "u0customer", "message_id": "1000000000000001", "message": message})
	if err != nil {
		t.Fatal(err)
	}
	c.attachStoryMedia(context.Background(), &event)
	if event.Media == nil || event.Message.Media == nil || event.Message.Media.Type != "image" || event.Media.ContentType != "image/jpeg" || event.Media.Size != 4 {
		t.Fatalf("media %#v %#v", event.Media, event.Message.Media)
	}
	reader, err := c.Download(context.Background(), *event.Media)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil || string(got) != string(f.image) {
		t.Fatalf("download %v %v", got, err)
	}
	if strings.Contains(string(event.Payload), "picture") || strings.Contains(string(event.Payload), "http") {
		t.Fatalf("provider URL leaked into public payload: %s", event.Payload)
	}
}

func TestStoryImageFailureIsNonBlocking(t *testing.T) {
	f := newFakeRubino(t)
	f.handlers["getProfileList"] = profileList
	f.handlers["getStory"] = func(map[string]any) any {
		return map[string]any{"stories": []any{map[string]any{"id": "story1", "full_snapshot_url": "https://evil.example/p"}}}
	}
	c := f.client(t, nil)
	event := domains.Event{Message: &domains.Message{Kind: "story_reply"}, Payload: json.RawMessage(`{"rubino_story":{"story_id":"story1","story_profile_id":"prof1"}}`)}
	c.attachStoryMedia(context.Background(), &event)
	if event.Media != nil {
		t.Fatal("foreign media host accepted")
	}
	forged := domains.ProviderMedia{Provider: domains.ProviderRubika, Version: 1, Size: 4, Data: json.RawMessage(`{"version":2,"account":"u0account","url":"https://evil.example/p","size":4}`)}
	if _, err := c.Download(context.Background(), forged); err == nil {
		t.Fatal("forged story reference downloaded")
	}
}

func TestForwardedRubinoPostGetsCaptionAndImage(t *testing.T) {
	f := newFakeRubino(t)
	f.handlers["getProfileList"] = profileList
	f.handlers["getProfilePosts"] = func(d map[string]any) any {
		if d["equal"] != true || d["max_id"] != "post1" || d["min_id"] != "post1" || d["target_profile_id"] != "owner1" {
			return map[string]any{"posts": []any{}}
		}
		return map[string]any{"posts": []any{map[string]any{"id": "post1", "caption": "New shoes", "post_profile_username": "shop", "full_file_url": f.srv.URL + "/video/v", "full_thumbnail_url": f.srv.URL + "/picture/t"}}}
	}
	c := f.client(t, nil)
	var message object
	if err := json.Unmarshal([]byte(`{"message_id":"1000000000000002","time":"1794500000","type":"RubinoPost","author_object_guid":"u0customer","rubino_post_data":{"post_id":"post1","post_profile_id":"owner1"}}`), &message); err != nil {
		t.Fatal(err)
	}
	event, err := c.projectMessage("u0customer", object{"action": "New", "object_guid": "u0customer", "message_id": "1000000000000002", "message": message})
	if err != nil || event.Message.Kind != "rubino_post" || !event.Message.Supported {
		t.Fatalf("projection %#v %v", event.Message, err)
	}
	c.attachStoryMedia(context.Background(), &event)
	var payload map[string]any
	_ = json.Unmarshal(event.Payload, &payload)
	post, _ := payload["rubino_post"].(map[string]any)
	if post["caption"] != "New shoes" || post["profile_username"] != "shop" || event.Media == nil || event.Message.Media.Type != "image" {
		t.Fatalf("post enrichment %s %#v", event.Payload, event.Media)
	}
	if strings.Contains(string(event.Payload), "http") {
		t.Fatalf("provider URL leaked: %s", event.Payload)
	}
	bad := &domains.Message{}
	if out := projectRubinoPostShare(map[string]any{"post_id": "../x"}, bad); bad.Supported || !strings.Contains(fmt.Sprint(out), "valid:false") {
		t.Fatalf("malformed post accepted %#v %v", out, bad)
	}
}
