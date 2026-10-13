package rubikameow

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func TestConversationDiscoveredDuringOutageDrainsAllPagesAcrossRestart(t *testing.T) {
	message := func(n int) object {
		return object{"message_id": strconv.Itoa(n), "time": 900 + n, "type": "Text", "text": "synthetic", "author_object_guid": "u0peer"}
	}
	fixture := func(method string, input object, _ bool) object {
		switch method {
		case "getMessagesInterval":
			return object{"state": "200", "messages": []any{message(204), message(205)}}
		case "getMessagesUpdates":
			return object{"new_state": "201", "updated_messages": []any{}}
		case "getMessages":
			if input.str("sort") == "FromMax" {
				end, _ := strconv.Atoi(input.str("max_id"))
				rows := []any{}
				for n := end; n >= max(1, end-99); n-- {
					rows = append(rows, message(n))
				}
				return object{"messages": rows, "has_continue": end > 100}
			}
			start, _ := strconv.Atoi(input.str("min_id"))
			if input.str("sort") != "FromMin" {
				t.Fatal("wrong continuation")
			}
			rows := []any{}
			end := min(start+99, 205)
			for n := start; n <= end; n++ {
				rows = append(rows, message(n))
			}
			return object{"messages": rows, "has_continue": end < 205}
		}
		t.Fatalf("unexpected method %s", method)
		return nil
	}
	raw := ""
	ids := []string{}
	seen := map[string]bool{}
	failCommit := false
	crash := errors.New("synthetic storage failure")
	sink := func(_ context.Context, b domains.EventBatch) error {
		if failCommit {
			return crash
		}
		if len(b.Checkpoints) != 1 || b.Checkpoints[0].Expected != raw {
			t.Fatal("checkpoint advanced without acceptance")
		}
		for _, event := range b.Events {
			if event.Type == "message" {
				if seen[event.ID] {
					t.Fatal("duplicate durable message")
				}
				seen[event.ID] = true
				ids = append(ids, event.MessageID)
			}
		}
		raw = b.Checkpoints[0].Next
		return nil
	}
	client := func() *Client {
		c, _ := newRPCFixture(t, fixture)
		c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) { return raw, nil }
		c.startedAt = 1000
		c.names.Remember("u0self", "u0peer", "Peer")
		return c
	}
	p := pendingChat{GUID: "u0peer", LastMessage: "205", Backfill: true}
	c := client()
	var err error
	for i := 0; i < 4 && len(ids) == 0; i++ {
		_, err = c.recoverPeer(context.Background(), p, c.enrichSink(sink))
		if !errors.Is(err, errMoreMessages) {
			t.Fatal("baseline did not retain continuation", err)
		}
	}
	if len(ids) != 100 || ids[0] != "100" {
		t.Fatalf("first outage page skipped or snapshot published first: %v ids=%v", err, ids)
	}
	before := raw
	failCommit = true
	_, err = c.recoverPeer(context.Background(), p, c.enrichSink(sink))
	if !errors.Is(err, crash) || raw != before {
		t.Fatal("failed page advanced progress")
	}
	failCommit = false
	c = client() // Fresh client, same durable cursor and connection boundary.
	for i := 0; i < 2; i++ {
		_, err = c.recoverPeer(context.Background(), p, c.enrichSink(sink))
		if err != nil && !errors.Is(err, errMoreMessages) {
			t.Fatal(err)
		}
	}
	if len(ids) != 106 {
		t.Fatalf("missing outage messages: %d", len(ids))
	}
	for i, id := range ids {
		if id != strconv.Itoa(100+i) {
			t.Fatal("outage message order changed")
		}
	}
	cp, err := parseCheckpoint(raw, "u0self")
	if err != nil || cp.LastMessage != "205" {
		t.Fatal("wrong final watermark")
	}
}

func TestRetainedPeerGapIsVisibleWhileMoreMessagePagesRemain(t *testing.T) {
	c, _ := newRPCFixture(t, func(method string, _ object, _ bool) object {
		if method == "getMessagesUpdates" {
			return object{"new_state": "101", "updated_messages": []any{}}
		}
		return object{"messages": []any{object{"message_id": "42", "time": 1000, "type": "Text", "text": "synthetic", "author_object_guid": "u0peer"}}, "has_continue": true}
	})
	peer := checkpoint{Version: 2, Account: "u0self", Peer: "u0peer", State: "100", LastMessage: "41", Gap: true, GapReason: "message_state_expired"}
	c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) { return checkpointJSON(peer), nil }
	account := checkpoint{Version: 2, Account: "u0self", State: "100", Pending: []pendingChat{{GUID: "u0peer", LastMessage: "45"}}}
	next, err := c.recoverPage(context.Background(), checkpointJSON(account), account, func(context.Context, domains.EventBatch) error { return nil })
	if err != nil || !next.Gap || next.GapReason != "message_state_expired" || len(next.Pending) != 1 {
		t.Fatalf("gap hidden while paging: %v %+v", err, next)
	}
	c.setRecoveryStatus(next)
	if c.Status().Recovery != "gap_detected" {
		t.Fatal("pending work erased retained gap")
	}
}
