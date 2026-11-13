package rubikameow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
)

func TestDirectRecoveryDiscoveryAndRetriesExcludeOtherPeers(t *testing.T) {
	c, _ := newRPCFixture(t, func(method string, input object, _ bool) object {
		switch method {
		case "getChatsUpdates":
			return object{"new_state": "101", "chats": []any{
				object{"object_guid": "c0channel", "last_message_id": "99"},
				object{"object_guid": "g0group", "last_message_id": "99"},
				object{"object_guid": "b0bot", "last_message_id": "99"},
				object{"object_guid": "s0service", "last_message_id": "99"},
				object{"object_guid": "z0unknown", "last_message_id": "99"},
				object{"object_guid": "u0peer", "last_message_id": "42"},
			}, "deleted_chats": []any{"c0deleted"}}
		case "getMessagesInterval":
			require.Equal(t, "u0peer", input.str("object_guid"))
			return object{"state": "200", "messages": []any{object{"message_id": "42", "time": 900}}}
		default:
			t.Fatalf("out-of-scope or unnecessary RPC: %s %v", method, input)
			return nil
		}
	})
	c.cfg.DirectMessagesOnly, c.startedAt = true, 1000
	cp := checkpoint{Version: 2, Account: "u0self", State: "100", StartedAt: 1000, Pending: []pendingChat{{GUID: "c0unavailable", RetryAt: time.Now().Add(time.Hour).Unix()}}}
	stored := map[string]string{c.accountCheckpointScope(): checkpointJSON(cp)}
	c.cfg.LoadCheckpoint = func(_ context.Context, scope string) (string, error) { return stored[scope], nil }
	sink := func(_ context.Context, batch domains.EventBatch) error {
		for _, event := range batch.Events {
			if event.Type != "connection.recovery" && event.Type != "protocol.unsupported_update" {
				require.Equal(t, "user", event.Peer.Type)
			}
			require.NotEqual(t, "message", event.Type, "pre-attachment history escaped")
		}
		for _, tr := range batch.Checkpoints {
			require.Equal(t, stored[tr.Scope], tr.Expected)
			stored[tr.Scope] = tr.Next
		}
		return nil
	}
	next, err := c.recoverPage(context.Background(), stored[c.accountCheckpointScope()], cp, sink)
	require.NoError(t, err)
	require.Len(t, next.Pending, 1)
	require.Equal(t, "u0peer", next.Pending[0].GUID)
	next, err = c.recoverPage(context.Background(), stored[c.accountCheckpointScope()], next, sink)
	require.NoError(t, err)
	require.Empty(t, next.Pending)
	c.setRecoveryStatus(next)
	require.Equal(t, "current", c.Status().Recovery)
	require.Empty(t, c.Status().LastError)
	require.False(t, c.Status().UnsupportedUpdatesObserved)
}

func TestDirectSocketIgnoresGroupsOldMessagesAndUnrelatedFrames(t *testing.T) {
	c, _ := newRPCFixture(t, func(method string, _ object, _ bool) object {
		t.Fatalf("unexpected enrichment/RPC for excluded content: %s", method)
		return nil
	})
	c.cfg.DirectMessagesOnly, c.startedAt = true, 1000
	c.names.Remember("u0self", "u0peer", "Peer")
	key, _ := authKey(fixtureAuth)
	accept := func(data object) []domains.Event {
		t.Helper()
		encrypted, err := encrypt(data, key)
		require.NoError(t, err)
		frame, err := json.Marshal(object{"type": "messenger", "data_enc": encrypted})
		require.NoError(t, err)
		var events []domains.Event
		err = c.acceptFrame(context.Background(), frame, key, c.enrichSink(func(_ context.Context, batch domains.EventBatch) error {
			events = append(events, batch.Events...)
			return nil
		}))
		require.NoError(t, err)
		return events
	}
	// Invalid group/channel payloads must be filtered before projection. Old
	// direct attachments also cannot trigger a parser failure or coverage warning.
	updates := []any{}
	for _, guid := range []string{"g0group", "c0channel", "b0bot", "s0service"} {
		updates = append(updates, object{"object_guid": guid, "action": "Unknown", "message": "invalid"})
	}
	updates = append(updates,
		object{"object_guid": "u0peer", "action": "New", "message_id": "1", "message": object{"time": 999, "file_inline": object{"invalid": true}}},
		object{"object_guid": "u0peer", "action": "Edit", "message_id": "1", "message": object{"time": 999, "file_inline": object{"invalid": true}}},
		object{"object_guid": "u0peer", "action": "New", "message_id": "2", "message": object{"time": 1000, "type": "Text", "text": "new", "author_object_guid": "u0peer"}},
	)
	events := accept(object{"user_guid": "u0self", "message_updates": updates})
	require.Len(t, events, 1)
	require.Equal(t, "2", events[0].MessageID)
	require.False(t, c.Status().UnsupportedUpdatesObserved)
	diagnostics := accept(object{"user_guid": "u0self", "unrelated_updates": []any{object{"value": "unknown"}}})
	require.Len(t, diagnostics, 1)
	require.Equal(t, "protocol.unsupported_update", diagnostics[0].Type)
	require.False(t, c.Status().UnsupportedUpdatesObserved)
	// A real unsupported direct message must still be reported.
	accept(object{"message_updates": []any{object{"object_guid": "u0peer", "action": "New", "message_id": "3", "message": object{"time": 1001, "type": "FutureType", "author_object_guid": "u0peer"}}}})
	require.True(t, c.Status().UnsupportedUpdatesObserved)
}

func TestDirectScopeUpgradePreservesBoundaryWatermarksAndRelevantGaps(t *testing.T) {
	for _, userGap := range []bool{false, true} {
		t.Run(fmt.Sprintf("user_gap_%t", userGap), func(t *testing.T) {
			c, _ := newRPCFixture(t, func(string, object, bool) object { return nil })
			c.cfg.DirectMessagesOnly = true
			old := checkpoint{Version: 2, Account: "u0self", State: "100", StartedAt: 1000, Gap: true, GapReason: "message_state_expired", Pending: []pendingChat{{GUID: "c0bad", RetryAt: 2000}, {GUID: "u0peer", LastMessage: "50"}}}
			peer := checkpoint{Version: 2, Account: "u0self", Peer: "u0peer", State: "200", LastMessage: "42", Gap: userGap}
			if userGap {
				peer.GapReason = "message_state_expired"
			}
			stored := map[string]string{accountScope: checkpointJSON(old), "rubika.messages:u0peer": checkpointJSON(peer)}
			c.cfg.LoadCheckpoint = func(_ context.Context, scope string) (string, error) { return stored[scope], nil }
			c.cfg.LoadCheckpointPage = func(_ context.Context, prefix, after string) (map[string]string, error) {
				require.Equal(t, "rubika.messages:u0", prefix)
				result := map[string]string{}
				for scope, value := range stored {
					if strings.HasPrefix(scope, prefix) && scope > after {
						result[scope] = value
					}
				}
				return result, nil
			}
			crash := errors.New("storage unavailable")
			_, _, err := c.loadRecoveryCheckpoint(context.Background(), "u0self", func(context.Context, domains.EventBatch) error { return crash })
			require.ErrorIs(t, err, crash)
			require.Empty(t, stored[c.accountCheckpointScope()])
			raw, cp, err := c.loadRecoveryCheckpoint(context.Background(), "u0self", func(_ context.Context, batch domains.EventBatch) error {
				require.Empty(t, batch.Events)
				require.Len(t, batch.Checkpoints, 1)
				tr := batch.Checkpoints[0]
				require.Equal(t, c.accountCheckpointScope(), tr.Scope)
				require.Empty(t, tr.Expected)
				stored[tr.Scope] = tr.Next
				return nil
			})
			require.NoError(t, err)
			require.Equal(t, int64(1000), cp.StartedAt)
			require.Equal(t, "100", cp.State)
			require.True(t, cp.RefreshPeers)
			require.Equal(t, userGap, cp.Gap)
			require.Len(t, cp.Pending, 1)
			require.Equal(t, "u0peer", cp.Pending[0].GUID)
			require.Equal(t, checkpointJSON(old), stored[accountScope])
			require.Equal(t, checkpointJSON(peer), stored["rubika.messages:u0peer"])
			// A restart resumes the scoped cursor, without reimporting all-account
			// warnings or moving the first-attachment boundary forward.
			again, cp, err := c.loadRecoveryCheckpoint(context.Background(), "u0self", func(context.Context, domains.EventBatch) error { t.Fatal("unexpected reinitialization"); return nil })
			require.NoError(t, err)
			require.Equal(t, raw, again)
			require.Equal(t, int64(1000), cp.StartedAt)
		})
	}
}

func TestDirectScopeKeepsAccountGapsAndDoesNotHideUserFailures(t *testing.T) {
	c, _ := newRPCFixture(t, func(string, object, bool) object {
		return object{"_fixture_error": object{"status": "ERROR", "status_det": "ACCESS_DENIED"}}
	})
	c.cfg.DirectMessagesOnly = true
	cp := checkpoint{Version: 2, Account: "u0self", State: "100", StartedAt: 1000, Gap: true, GapReason: "chat_state_expired", Pending: []pendingChat{{GUID: "u0peer", LastMessage: "42", Backfill: true}}}
	c.cfg.LoadCheckpoint = func(_ context.Context, scope string) (string, error) {
		if scope == accountScope {
			return checkpointJSON(cp), nil
		}
		return "", nil
	}
	_, migrated, err := c.loadRecoveryCheckpoint(context.Background(), "u0self", func(context.Context, domains.EventBatch) error { return nil })
	require.NoError(t, err)
	require.Equal(t, "chat_state_expired", migrated.GapReason)
	next, err := c.recoverPage(context.Background(), checkpointJSON(cp), cp, func(context.Context, domains.EventBatch) error { return nil })
	require.NoError(t, err)
	c.setRecoveryStatus(next)
	require.Equal(t, "gap_detected", c.Status().Recovery)
	require.Equal(t, "RECOVERY_FAILED", c.Status().LastError)
	require.Greater(t, next.Pending[0].RetryAt, time.Now().Unix())
}

func TestLeavingDirectModeRetainsOriginalBoundary(t *testing.T) {
	c, _ := newRPCFixture(t, func(string, object, bool) object { return nil })
	previous := checkpoint{Version: 2, Account: "u0self", State: "100", StartedAt: 1000}
	c.cfg.LoadCheckpoint = func(_ context.Context, scope string) (string, error) {
		if scope == "rubika.direct.chats" {
			return checkpointJSON(previous), nil
		}
		return "", nil
	}
	_, cp, err := c.loadRecoveryCheckpoint(context.Background(), "u0self", func(_ context.Context, batch domains.EventBatch) error {
		require.Equal(t, accountScope, batch.Checkpoints[0].Scope)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, int64(1000), cp.StartedAt)
	require.Equal(t, "100", cp.State)
	require.True(t, cp.RefreshPeers)
}
