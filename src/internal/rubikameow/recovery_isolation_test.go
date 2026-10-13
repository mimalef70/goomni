package rubikameow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

func TestUnavailablePeerDoesNotStarveNewConversationDiscovery(t *testing.T) {
	calls := []string{}
	c, _ := newRPCFixture(t, func(method string, input object, _ bool) object {
		calls = append(calls, method+":"+input.str("object_guid"))
		switch method {
		case "getMessagesUpdates":
			if input.str("object_guid") == "c0unavailable" {
				return object{"_fixture_error": object{"status": "ERROR", "status_det": "INVALID_AUTH"}}
			}
			return object{"new_state": "101", "updated_messages": []any{}}
		case "getUserInfo":
			if input.str("user_guid") != "u0self" {
				t.Fatal("auth validation escaped bound self")
			}
			return object{"user": object{"user_guid": "u0self"}}
		case "getMessages":
			return object{"messages": []any{object{"message_id": "42", "time": 1700000000, "type": "Text", "text": "synthetic", "author_object_guid": input.str("object_guid")}}, "has_continue": false}
		case "getChatsUpdates":
			if input.str("state") != "100" {
				t.Fatal("account cursor advanced without durable discovery")
			}
			return object{"new_state": "101", "chats": []any{object{"object_guid": "u0new", "last_message_id": "42"}, object{"object_guid": "c0unavailable", "last_message_id": "41"}, object{"object_guid": "c0unavailable", "last_message_id": "39"}}}
		case "getMessagesInterval":
			return object{"state": "100", "messages": []any{object{"message_id": "41", "time": 900, "type": "Text"}}}
		}
		t.Fatalf("unexpected method %s", method)
		return nil
	})
	c.startedAt = 1000
	account := checkpoint{Version: 2, Account: "u0self", State: "100", StartedAt: 1000, Pending: []pendingChat{{GUID: "c0unavailable", LastMessage: "40", Backfill: true}, {GUID: "u0known", LastMessage: "42"}}}
	stored := map[string]string{accountScope: checkpointJSON(account)}
	for _, peer := range []string{"c0unavailable", "u0known"} {
		stored["rubika.messages:"+peer] = checkpointJSON(checkpoint{Version: 2, Account: "u0self", Peer: peer, State: "100", LastMessage: "41"})
	}
	c.cfg.LoadCheckpoint = func(_ context.Context, scope string) (string, error) { return stored[scope], nil }
	accepted := map[string]bool{}
	sink := func(_ context.Context, b domains.EventBatch) error {
		for _, tr := range b.Checkpoints {
			if stored[tr.Scope] != tr.Expected {
				t.Fatal("non-durable cursor used")
			}
			stored[tr.Scope] = tr.Next
		}
		for _, e := range b.Events {
			if e.Type == "message" {
				if accepted[e.ID] {
					t.Fatal("duplicate durable message")
				}
				accepted[e.ID] = true
			}
		}
		return nil
	}
	next, err := c.recoverPage(context.Background(), stored[accountScope], account, sink)
	if err != nil || len(next.Pending) != 1 || next.Pending[0].GUID != "c0unavailable" || next.Pending[0].RetryAt <= time.Now().Unix() || !next.PollChats || len(accepted) != 1 {
		t.Fatalf("healthy peer starved: %+v %v", next, err)
	}
	c.setRecoveryStatus(next)
	if c.Status().Auth == "auth_required" || c.Status().Recovery != "degraded" || c.Status().LastError != "RECOVERY_FAILED" || c.Status().RecoveryIssue != "message_peer_unavailable" || next.Gap {
		t.Fatal("unavailable peer erased auth or falsely claimed coverage/loss")
	}
	// A fresh process resumes the persisted scheduler decision, not a new cutoff.
	next, err = parseCheckpoint(stored[accountScope], "u0self")
	if err != nil {
		t.Fatal(err)
	}
	next, err = c.recoverPage(context.Background(), stored[accountScope], next, sink)
	if err != nil || len(next.Pending) != 2 || next.Pending[0].LastMessage != "41" || next.Pending[0].RetryAt == 0 || next.Pending[1].GUID != "u0new" || next.PollChats {
		t.Fatalf("new discovery starved or duplicate peer/cursor rewound: %+v %v", next, err)
	}
	next, err = c.recoverPage(context.Background(), stored[accountScope], next, sink)
	if err != nil || len(next.Pending) != 1 || next.Pending[0].GUID != "c0unavailable" || len(accepted) != 2 {
		t.Fatalf("new conversation stalled: %+v %v", next, err)
	}
	if len(calls) != 7 {
		t.Fatalf("deferred peer retried before deadline: %v", calls)
	}
}

func TestPeerIsolationDoesNotSwallowAcceptanceOrGlobalFailures(t *testing.T) {
	for _, tc := range []struct {
		name, providerStatus     string
		failSink, boolSelfReject bool
	}{
		{name: "storage failure", failSink: true},
		{name: "storage domain failure", failSink: true, providerStatus: "domain"},
		{name: "global throttling", providerStatus: "TOO_REQUESTS"},
		{name: "actual self auth loss", providerStatus: "INVALID_AUTH", boolSelfReject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newRPCFixture(t, func(method string, _ object, _ bool) object {
				if method == "getUserInfo" || !tc.failSink {
					return object{"_fixture_error": object{"status": "ERROR", "status_det": tc.providerStatus}}
				}
				return object{"new_state": "101", "updated_messages": []any{}}
			})
			c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) {
				return checkpointJSON(checkpoint{Version: 2, Account: "u0self", Peer: "u0peer", State: "100", LastMessage: "41"}), nil
			}
			cp := checkpoint{Version: 2, Account: "u0self", State: "100", Pending: []pendingChat{{GUID: "u0peer", LastMessage: "41"}}}
			failure := error(errors.New("synthetic storage failure"))
			if tc.providerStatus == "domain" {
				failure = domains.E("PROVIDER_PROTOCOL_ERROR", "synthetic acceptance failure", 502)
			}
			accountCommits := 0
			next, err := c.recoverPage(context.Background(), checkpointJSON(cp), cp, func(_ context.Context, b domains.EventBatch) error {
				if b.Checkpoints[0].Scope == accountScope {
					accountCommits++
				}
				return failure
			})
			if err == nil || accountCommits != 0 || next.PollChats || next.Pending[0].RetryAt != 0 {
				t.Fatalf("global failure hidden/advanced: %+v %v", next, err)
			}
			if tc.failSink && !errors.Is(err, failure) {
				t.Fatal("acceptance failure replaced")
			}
		})
	}
}

func TestDeferredRecoveryStatusRetainsRealGapAndRejectsInvalidRetry(t *testing.T) {
	c, _ := newRPCFixture(t, func(string, object, bool) object { return nil })
	cp := checkpoint{Version: 2, Account: "u0self", Gap: true, GapReason: "message_state_expired", Pending: []pendingChat{{GUID: "u0peer", RetryAt: 1}}}
	c.setRecoveryStatus(cp)
	if c.Status().Recovery != "gap_detected" || c.Status().RecoveryIssue != "message_state_expired" || c.Status().LastError != "RECOVERY_FAILED" {
		t.Fatal("retained gap hidden by retry")
	}
	cp.Pending[0].RetryAt = -1
	if _, err := parseCheckpoint(checkpointJSON(cp), cp.Account); err == nil {
		t.Fatal("negative retry deadline accepted")
	}
}
