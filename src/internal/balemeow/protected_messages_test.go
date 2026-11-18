package balemeow

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

// Synthetic values only. None of these are real PINs, tokens or OTPs.
const (
	syntheticPIN       = "246810"
	syntheticRamzToken = "synthetic.ramz.token"
	syntheticLocked    = "SYNTHETIC-LOCKED-BODY"
	syntheticOTPText   = "رمز یکبار مصرف آزمایشی: 000000 — synthetic"
	protectedRID       = int64(9007199254740993)
	protectedDate      = int64(1720000000001)
)

func protectedHistory(inner *wire.Message) *wire.HistoryItem {
	return &wire.HistoryItem{SenderId: 42, Rid: protectedRID, Date: protectedDate, Message: &wire.Message{Protected: &wire.ProtectedMessage{Message: inner}}}
}

func unlockBody(pin string) json.RawMessage {
	return json.RawMessage(`{"peer":{"type":"user","id":"42"},"message_id":"9007199254740993","pin":"` + pin + `"}`)
}

func metadataValue(m *wire.Metadata, key string) string {
	for _, item := range m.GetItems() {
		if item.GetKey() == key {
			return item.GetValue().GetStringValue()
		}
	}
	return ""
}

type unlockFixture struct {
	checks, fetches atomic.Int32
	checkCode       atomic.Int32
	fetchCode       int32
	silentCheck     bool
	lateCheckCode   atomic.Int32 // reply after the client's request timeout
	metadataHistory []*wire.HistoryItem
	history         *wire.HistoryItem
}

func newUnlockClient(t *testing.T, f *unlockFixture) *Client {
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
		// Provider free-form text deliberately echoes secrets; it must never surface.
		freeForm := "provider text " + syntheticPIN + " " + syntheticRamzToken
		switch r.Service + "/" + r.Method {
		case "bale.messaging.v2.Messaging/LoadHistory":
			reply.Payload = marshal(t, &wire.HistoryResponse{History: f.metadataHistory})
		case "bale.users.v1.Users/LoadUsers":
			reply.Payload = marshal(t, &wire.AccountUsersResponse{Users: []*wire.User{{Id: 42, IsBot: &wire.BoolValue{Value: true}}}})
		case "bale.ramz.v1.Ramz/CheckPassword":
			f.checks.Add(1)
			q := &wire.RamzCheckPasswordRequest{}
			if decode(r.Payload, q) != nil || q.Password != syntheticPIN || q.ServicesType != 1 {
				t.Errorf("invalid CheckPassword request")
			}
			if metadataValue(r.Metadata, "bale_ramz_token") != "" || metadataValue(r.Metadata, "auth-jwt") == "" {
				t.Errorf("CheckPassword must use the account session and no Ramz token")
			}
			if f.silentCheck {
				return
			}
			if code := f.lateCheckCode.Swap(0); code != 0 {
				go func() {
					time.Sleep(250 * time.Millisecond)
					fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Error: &wire.Status{Code: code}}})
				}()
				return
			}
			if code := f.checkCode.Load(); code != 0 {
				reply.Error = &wire.Status{Code: code, Message: freeForm}
			} else {
				reply.Payload = marshal(t, &wire.RamzCheckPasswordResponse{Token: syntheticRamzToken})
			}
		case "bale.messaging.v2.Messaging/FetchProtectedMessage":
			f.fetches.Add(1)
			q := &wire.FetchProtectedMessageRequest{}
			if decode(r.Payload, q) != nil || q.Peer.GetType() != 4 || q.MessageId == nil || q.MessageId.Sequence == nil || q.MessageId.GetSequence() != 0 || q.Peer.GetId() != 42 || q.MessageId.GetRid() != protectedRID || q.MessageId.GetDate() != protectedDate {
				t.Errorf("invalid FetchProtectedMessage request")
			}
			if metadataValue(r.Metadata, "bale_ramz_token") != syntheticRamzToken {
				t.Errorf("FetchProtectedMessage must carry the token from this request's PIN check")
			}
			if f.fetchCode != 0 {
				reply.Error = &wire.Status{Code: f.fetchCode, Message: freeForm}
			} else {
				reply.Payload = marshal(t, &wire.FetchProtectedMessageResponse{History: f.history})
			}
		default:
			t.Errorf("unexpected provider call %s/%s", r.Service, r.Method)
			return
		}
		fake.send(ws, &wire.ServerMessage{Response: reply})
	})
	c := fake.client()
	c.opts.ResolveMessageDate = func(_ context.Context, _ domains.Peer, mid string) (int64, bool, error) {
		if mid == sid(protectedRID) {
			return protectedDate, true, nil
		}
		return 0, false, nil
	}
	connectTest(t, c, acceptingSink)
	c.rememberRef("user", &wire.PeerRef{Id: 42, AccessHash: 73})
	return c
}

func requireNoSecrets(t *testing.T, text string) {
	t.Helper()
	for _, secret := range []string{syntheticPIN, syntheticRamzToken, syntheticOTPText, syntheticLocked, "provider text"} {
		require.NotContains(t, text, secret)
	}
}

func TestProtectedMessageLiteralWireIsRecognizedAndLocked(t *testing.T) {
	// Independently specified from the official web encoder: Message field 27
	// (tag 218), ProtectedMessage.message=1, inner TextMessage field 15.
	raw, err := hex.DecodeString("da010c0a0a7a080a066c6f636b6564")
	require.NoError(t, err)
	m := &wire.Message{}
	require.NoError(t, decode(raw, m))
	require.Equal(t, "locked", m.GetProtected().GetMessage().GetText().GetText())
	require.NoError(t, validateMessage(m))
	require.JSONEq(t, `{"kind":"protected_message","locked":true}`, string(messagePayload(m)))

	// A malformed union that also carries text is rejected, and even the bare
	// decoder classifies it as protected rather than ordinary text.
	both := &wire.Message{Text: &wire.TextMessage{Text: syntheticLocked}, Protected: &wire.ProtectedMessage{}}
	require.Error(t, validateMessage(both))
	require.Equal(t, "protected_message", contentBody(both, 0)["kind"])
	nestedInvalid := &wire.Message{Protected: &wire.ProtectedMessage{Message: &wire.Message{Text: &wire.TextMessage{}, Empty: &wire.Empty{}}}}
	require.Error(t, validateMessage(nestedInvalid))
}

func TestProtectedMessageEventsAndWebhookPayloadStayLocked(t *testing.T) {
	inner := &wire.Message{Text: &wire.TextMessage{Text: syntheticLocked}}
	event := receivedMessage(t, &wire.Message{Protected: &wire.ProtectedMessage{Message: inner}}, nil)
	require.Equal(t, "protected_message", event.Message.Kind)
	require.Equal(t, "[Protected message]", event.Message.Body)
	require.True(t, event.Message.Supported)
	require.Equal(t, "-9007199254740993", event.Message.ID)
	require.Equal(t, "77", event.Message.ChatID)
	require.Nil(t, event.Media)
	body, err := json.Marshal(event)
	require.NoError(t, err)
	requireNoSecrets(t, string(body))
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(body, &envelope))
	require.Equal(t, map[string]any{"kind": "protected_message", "locked": true}, envelope["content"])
	require.Equal(t, "protected_message", envelope["payload"].(map[string]any)["kind"])

	// Ordinary text and genuinely unknown union arms keep their behavior.
	text := receivedMessage(t, &wire.Message{Text: &wire.TextMessage{Text: "ordinary"}}, nil)
	require.Equal(t, "text", text.Message.Kind)
	require.Equal(t, "ordinary", text.Message.Body)
	unknown := &wire.Message{}
	// Field 30 is an unreviewed union arm; it must remain explicitly unsupported.
	require.NoError(t, decode(protowire.AppendBytes(protowire.AppendTag(nil, 30, protowire.BytesType), []byte{0x0a, 0x00}), unknown))
	require.JSONEq(t, `{"kind":"unsupported"}`, string(messagePayload(unknown)))
	projected := receivedMessage(t, unknown, nil)
	require.Equal(t, "[Unsupported message]", projected.Message.Body)
	require.False(t, projected.Message.Supported)
}

func TestProtectedMessageHistoryStaysLocked(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if r := m.Request; r != nil && r.Method == "LoadHistory" {
			item := protectedHistory(&wire.Message{Text: &wire.TextMessage{Text: syntheticLocked}})
			fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Payload: marshal(t, &wire.HistoryResponse{History: []*wire.HistoryItem{item}})}})
		}
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	out, err := c.Call(context.Background(), "chat.history", json.RawMessage(`{"peer":{"type":"user","id":"42"},"limit":1}`))
	require.NoError(t, err)
	requireNoSecrets(t, string(out))
	var page struct {
		Messages []historyMessage `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(out, &page))
	require.Len(t, page.Messages, 1)
	require.Equal(t, "9007199254740993", page.Messages[0].ID)
	require.Equal(t, "protected_message", page.Messages[0].Message.Kind)
	require.JSONEq(t, `{"kind":"protected_message","locked":true}`, string(page.Messages[0].Payload))
}

func TestProtectedUnlockLiteralRequests(t *testing.T) {
	// Independent of the generated encoder: password=1 string, services_type=2 varint.
	check, err := hex.DecodeString("0a063234363831301001")
	require.NoError(t, err)
	require.Equal(t, check, marshal(t, &wire.RamzCheckPasswordRequest{Password: syntheticPIN, ServicesType: 1}))
	peer := protowire.AppendVarint(protowire.AppendTag(nil, 1, protowire.VarintType), 4)
	peer = protowire.AppendVarint(protowire.AppendTag(peer, 2, protowire.VarintType), 42)
	id := protowire.AppendVarint(protowire.AppendTag(nil, 1, protowire.VarintType), uint64(protectedDate))
	id = protowire.AppendVarint(protowire.AppendTag(id, 2, protowire.VarintType), uint64(protectedRID))
	id = protowire.AppendVarint(protowire.AppendTag(id, 3, protowire.VarintType), 0)
	fetch := protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), peer)
	fetch = protowire.AppendBytes(protowire.AppendTag(fetch, 2, protowire.BytesType), id)
	require.Equal(t, fetch, marshal(t, &wire.FetchProtectedMessageRequest{Peer: &wire.ProtectedFetchPeer{Type: 4, Id: 42}, MessageId: &wire.ProtectedFetchPosition{Date: protectedDate, Rid: protectedRID, Sequence: new(int64)}}))
}

func TestProtectedUnlockReturnsProviderContentExactly(t *testing.T) {
	f := &unlockFixture{history: protectedHistory(&wire.Message{Text: &wire.TextMessage{Text: syntheticOTPText}})}
	c := newUnlockClient(t, f)
	out, err := c.Call(context.Background(), "message.protected.unlock", unlockBody(syntheticPIN))
	require.NoError(t, err)
	require.NotContains(t, string(out), syntheticPIN)
	require.NotContains(t, string(out), syntheticRamzToken)
	var result struct {
		Peer      domains.Peer   `json:"peer"`
		MessageID string         `json:"message_id"`
		Date      string         `json:"date"`
		SenderID  string         `json:"sender_id"`
		Locked    bool           `json:"locked"`
		Content   map[string]any `json:"content"`
	}
	require.NoError(t, json.Unmarshal(out, &result))
	require.Equal(t, domains.Peer{Type: "user", ID: "42"}, result.Peer)
	require.Equal(t, "9007199254740993", result.MessageID)
	require.Equal(t, "1720000000001", result.Date)
	require.Equal(t, "42", result.SenderID)
	require.False(t, result.Locked)
	require.Equal(t, map[string]any{"kind": "text", "message": syntheticOTPText}, result.Content)
	require.EqualValues(t, 1, f.checks.Load())
	require.EqualValues(t, 1, f.fetches.Load())

	// Every unlock needs its own PIN check: no PIN or token reuse, including
	// after a reconnect.
	_, err = c.Call(context.Background(), "message.protected.unlock", unlockBody(syntheticPIN))
	require.NoError(t, err)
	require.NoError(t, c.Disconnect(context.Background()))
	connectTest(t, c, acceptingSink)
	_, err = c.Call(context.Background(), "message.protected.unlock", unlockBody(syntheticPIN))
	require.NoError(t, err)
	require.EqualValues(t, 3, f.checks.Load())
	require.EqualValues(t, 3, f.fetches.Load())
}

func TestProtectedUnlockPINDecisionsAreNotRetriedAndKeepSession(t *testing.T) {
	for _, tc := range []struct {
		code int32
		want string
		http int
	}{
		{16, "PROTECTED_PIN_REJECTED", 403},
		{10, "PROTECTED_PIN_ATTEMPTS_LIMITED", 429},
		{7, "PROTECTED_PIN_VERIFICATION_REQUIRED", 403},
		{8, "PROVIDER_RATE_LIMITED", 429},
		{13, "PROVIDER_REJECTED", 502},
	} {
		t.Run(tc.want, func(t *testing.T) {
			f := &unlockFixture{history: protectedHistory(&wire.Message{Text: &wire.TextMessage{Text: syntheticOTPText}})}
			f.checkCode.Store(tc.code)
			c := newUnlockClient(t, f)
			_, err := c.Call(context.Background(), "message.protected.unlock", unlockBody(syntheticPIN))
			var de *domains.Error
			require.True(t, errors.As(err, &de))
			require.Equal(t, tc.want, de.Code)
			require.Equal(t, tc.http, de.HTTP)
			require.False(t, de.Retryable)
			requireNoSecrets(t, err.Error()+de.Message)
			require.EqualValues(t, 1, f.checks.Load(), "a PIN attempt is never retried")
			require.Zero(t, f.fetches.Load(), "content is never fetched without a successful PIN check")
			// A rejected PIN is not a revoked account session.
			require.NotEqual(t, "auth_required", c.Status().Auth)
			f.checkCode.Store(0)
			_, err = c.Call(context.Background(), "message.protected.unlock", unlockBody(syntheticPIN))
			require.NoError(t, err, "connection must survive a rejected PIN")
		})
	}
}

func TestProtectedUnlockFetchOutcomes(t *testing.T) {
	text := &wire.Message{Text: &wire.TextMessage{Text: syntheticOTPText}}
	mismatch := protectedHistory(text)
	mismatch.Rid++
	otherSender := protectedHistory(text)
	otherSender.SenderId = 43
	for _, tc := range []struct {
		name    string
		code    int32
		history *wire.HistoryItem
		want    string
	}{
		{"not-found", 5, nil, "PROTECTED_MESSAGE_NOT_FOUND"},
		{"empty-history", 0, nil, "PROTECTED_FETCH_EMPTY"},
		{"invalid-history", 0, &wire.HistoryItem{Message: text}, "PROTECTED_FETCH_INVALID"},
		{"empty-wrapper", 0, protectedHistory(nil), "PROTECTED_CONTENT_EMPTY"},
		{"not-wrapped", 0, &wire.HistoryItem{SenderId: 42, Rid: protectedRID, Date: protectedDate, Message: text}, "PROTECTED_MESSAGE_UNSUPPORTED"},
		{"nested-lock", 0, protectedHistory(&wire.Message{Protected: &wire.ProtectedMessage{Message: text}}), "PROTECTED_MESSAGE_UNSUPPORTED"},
		{"unknown-inner", 0, protectedHistory(&wire.Message{Unsupported: &wire.Empty{}}), "PROTECTED_MESSAGE_UNSUPPORTED"},
		{"other-message", 0, mismatch, "PROTECTED_IDENTITY_MISMATCH"},
		{"other-chat", 0, otherSender, "PROTECTED_IDENTITY_MISMATCH"},
		{"token-rejected", 16, nil, "PROTECTED_UNLOCK_REJECTED"},
		{"rate-limited", 8, nil, "PROVIDER_RATE_LIMITED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &unlockFixture{fetchCode: tc.code, history: tc.history}
			c := newUnlockClient(t, f)
			_, err := c.Call(context.Background(), "message.protected.unlock", unlockBody(syntheticPIN))
			require.Equal(t, tc.want, codeOf(err))
			requireNoSecrets(t, err.Error())
			require.EqualValues(t, 1, f.checks.Load())
			require.EqualValues(t, 1, f.fetches.Load())
			require.NotEqual(t, "auth_required", c.Status().Auth)
		})
	}
}

func TestProtectedUnlockTimeoutIsNotRetried(t *testing.T) {
	f := &unlockFixture{silentCheck: true}
	c := newUnlockClient(t, f)
	_, err := c.Call(context.Background(), "message.protected.unlock", unlockBody(syntheticPIN))
	var de *domains.Error
	require.True(t, errors.As(err, &de))
	require.Equal(t, "PROTECTED_UNLOCK_INCOMPLETE", de.Code)
	require.False(t, de.Retryable)
	require.False(t, de.Ambiguous)
	require.EqualValues(t, 1, f.checks.Load())
	require.Zero(t, f.fetches.Load())
}

func TestLateRejectedPINAfterTimeoutKeepsSession(t *testing.T) {
	f := &unlockFixture{history: protectedHistory(&wire.Message{Text: &wire.TextMessage{Text: syntheticOTPText}})}
	f.lateCheckCode.Store(16)
	c := newUnlockClient(t, f)
	_, err := c.Call(context.Background(), "message.protected.unlock", unlockBody(syntheticPIN))
	require.Equal(t, "PROTECTED_UNLOCK_INCOMPLETE", codeOf(err))
	time.Sleep(400 * time.Millisecond) // the late UNAUTHENTICATED reply has now arrived
	require.NotEqual(t, "auth_required", c.Status().Auth)
	_, err = c.Call(context.Background(), "message.protected.unlock", unlockBody(syntheticPIN))
	require.NoError(t, err, "a late PIN decision must not revoke the account session")
	require.EqualValues(t, 2, f.checks.Load())
}

func TestProtectedUnlockRejectsInvalidInputBeforeNetwork(t *testing.T) {
	f := &unlockFixture{}
	c := newUnlockClient(t, f)
	for _, body := range []string{
		`{"peer":{"type":"group","id":"42"},"message_id":"1","pin":"246810"}`,
		`{"peer":{"type":"user","id":"42"},"message_id":"1","pin":"24681"}`,
		`{"peer":{"type":"user","id":"42"},"message_id":"1","pin":"2468100"}`,
		`{"peer":{"type":"user","id":"42"},"message_id":"1","pin":"۲۴۶۸۱۰"}`,
		`{"peer":{"type":"user","id":"42"},"message_id":"1","pin":"24681a"}`,
		`{"peer":{"type":"user","id":"42"},"message_id":"0","pin":"246810"}`,
		`{"peer":{"type":"user","id":"42"},"message_id":"1","pin":"246810","token":"x"}`,
		`{"peer":{"type":"user","id":"42"},"message_id":"1","pin":"246810","pin":"135790"}`,
	} {
		_, err := c.Call(context.Background(), "message.protected.unlock", json.RawMessage(body))
		require.Equal(t, "INVALID_REQUEST", codeOf(err), body)
		requireNoSecrets(t, err.Error())
		require.NotContains(t, err.Error(), "135790")
	}
	require.Zero(t, f.checks.Load())
	require.Zero(t, f.fetches.Load())

	offline := New(Options{ResolveMessageDate: c.opts.ResolveMessageDate})
	offline.rememberRef("user", &wire.PeerRef{Id: 42, AccessHash: 73})
	_, err := offline.Call(context.Background(), "message.protected.unlock", unlockBody(syntheticPIN))
	require.Equal(t, "CONNECTION_UNAVAILABLE", codeOf(err))
}

func TestOrdinaryUnauthenticatedReplyStillRevokesSession(t *testing.T) {
	var fake *fakeWS
	fake = newFakeWS(t, func(ws *websocket.Conn, m *wire.ClientMessage) {
		if r := m.Request; r != nil {
			fake.send(ws, &wire.ServerMessage{Response: &wire.Response{Index: r.Index, Error: &wire.Status{Code: 16}}})
		}
	})
	c := fake.client()
	connectTest(t, c, acceptingSink)
	_, err := c.Call(context.Background(), "message.read", json.RawMessage(`{"peer":{"type":"user","id":"77"},"date":"123"}`))
	require.Equal(t, "AUTH_REQUIRED", codeOf(err))
	eventually(t, func() bool { return c.Status().Auth == "auth_required" })
	require.False(t, strings.Contains(err.Error(), "provider text"))
}

func TestProtectedUnlockResolvesDateFromStoredEvent(t *testing.T) {
	f := &unlockFixture{history: protectedHistory(&wire.Message{Text: &wire.TextMessage{Text: syntheticOTPText}})}
	c := newUnlockClient(t, f) // default resolver maps protectedRID -> protectedDate
	out, err := c.Call(context.Background(), "message.protected.unlock", unlockBody(syntheticPIN))
	require.NoError(t, err)
	require.Contains(t, string(out), syntheticOTPText)
	require.Contains(t, string(out), `"date":"`+sid(protectedDate)+`"`)
	require.EqualValues(t, 1, f.fetches.Load())
}

func TestProtectedUnlockResolvesCurrentNotPreviousMessage(t *testing.T) {
	// The resolver is keyed by message_id, so the current message's own date is
	// used, never a neighbouring message's.
	f := &unlockFixture{history: protectedHistory(&wire.Message{Text: &wire.TextMessage{Text: syntheticOTPText}})}
	c := newUnlockClient(t, f)
	var askedFor string
	c.opts.ResolveMessageDate = func(_ context.Context, _ domains.Peer, mid string) (int64, bool, error) {
		askedFor = mid
		return protectedDate, true, nil
	}
	_, err := c.Call(context.Background(), "message.protected.unlock", unlockBody(syntheticPIN))
	require.NoError(t, err)
	require.Equal(t, sid(protectedRID), askedFor)
}

func TestProtectedUnlockMissingMetadataIsNotExpiry(t *testing.T) {
	// No stored event and no explicit date: a clear metadata error, not a PIN
	// attempt and not an expiry claim.
	f := &unlockFixture{history: protectedHistory(&wire.Message{Text: &wire.TextMessage{Text: syntheticOTPText}})}
	c := newUnlockClient(t, f)
	c.opts.ResolveMessageDate = func(context.Context, domains.Peer, string) (int64, bool, error) { return 0, false, nil }
	_, err := c.Call(context.Background(), "message.protected.unlock", unlockBody(syntheticPIN))
	require.Equal(t, "PROTECTED_MESSAGE_METADATA_NOT_FOUND", codeOf(err))
	require.Zero(t, f.checks.Load(), "metadata must resolve before any PIN attempt")
	require.Zero(t, f.fetches.Load())

	// Resolver error is sanitised and does not spend a PIN attempt.
	c.opts.ResolveMessageDate = func(context.Context, domains.Peer, string) (int64, bool, error) {
		return 0, false, errors.New("synthetic db failure 246810")
	}
	_, err = c.Call(context.Background(), "message.protected.unlock", unlockBody(syntheticPIN))
	require.Equal(t, "PROTECTED_METADATA_LOOKUP_FAILED", codeOf(err))
	requireNoSecrets(t, err.Error())
	require.NotContains(t, err.Error(), "synthetic db failure")

	// No resolver hook at all, no date: still a clear metadata error.
	c.opts.ResolveMessageDate = nil
	_, err = c.Call(context.Background(), "message.protected.unlock", unlockBody(syntheticPIN))
	require.Equal(t, "PROTECTED_MESSAGE_METADATA_NOT_FOUND", codeOf(err))
}

func TestProtectedUnlockResolvesDateFromRecentHistory(t *testing.T) {
	item := protectedHistory(&wire.Message{Text: &wire.TextMessage{Text: syntheticOTPText}})
	item.Previous = &wire.MessagePosition{Date: 1, Rid: 2}
	c := newUnlockClient(t, &unlockFixture{history: item, metadataHistory: []*wire.HistoryItem{item}})
	c.opts.ResolveMessageDate = nil
	out, err := c.Call(context.Background(), "message.protected.unlock", unlockBody(syntheticPIN))
	require.NoError(t, err)
	require.Contains(t, string(out), syntheticOTPText)
}
