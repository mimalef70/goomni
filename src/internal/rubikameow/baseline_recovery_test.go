package rubikameow

import (
	"context"
	"errors"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

func TestOldArchiveEstablishesBoundaryWithoutScanningFromBeginning(t *testing.T) {
	calls := 0
	c, _ := newRPCFixture(t, func(method string, _ object, _ bool) object {
		calls++
		if method != "getMessagesInterval" {
			t.Fatal("old archive scanned unnecessarily")
		}
		return object{"state": "100", "messages": []any{object{"message_id": "9007199254740993", "time": 900, "type": "Text", "text": "old", "author_object_guid": "u0peer"}}}
	})
	c.startedAt = 1000
	raw := ""
	c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) { return raw, nil }
	sink := c.enrichSink(func(_ context.Context, b domains.EventBatch) error {
		for _, event := range b.Events {
			if event.Type == "message" {
				t.Fatal("old archive event accepted")
			}
		}
		raw = b.Checkpoints[0].Next
		return nil
	})
	_, err := c.recoverPeer(context.Background(), pendingChat{GUID: "u0peer", LastMessage: "9007199254740993", Backfill: true}, sink)
	cp, parseErr := parseCheckpoint(raw, "u0self")
	if err != nil || parseErr != nil || calls != 1 || cp.LastMessage != "9007199254740993" {
		t.Fatalf("wrong baseline %v %v %+v", err, parseErr, cp)
	}
}

func TestBackwardBoundaryPageRetainsCursorOnCommitFailure(t *testing.T) {
	c, _ := newRPCFixture(t, func(method string, input object, _ bool) object {
		if method != "getMessages" || input.str("sort") != "FromMax" || input.str("max_id") != "20" {
			t.Fatal("wrong bounded read")
		}
		return object{"messages": []any{object{"message_id": "15", "time": 900, "type": "Text", "text": "old", "author_object_guid": "u0peer"}}, "has_continue": true}
	})
	c.startedAt = 1000
	cp := checkpoint{Version: 2, Account: "u0self", Peer: "u0peer", State: "100", BaselineBefore: "20"}
	before := checkpointJSON(cp)
	crash := errors.New("commit failed")
	for i := 0; i < 2; i++ {
		err := c.recoverBaselinePage(context.Background(), pendingChat{GUID: "u0peer"}, before, cp, func(_ context.Context, b domains.EventBatch) error {
			next, err := parseCheckpoint(b.Checkpoints[0].Next, "u0self")
			if err != nil || b.Checkpoints[0].Expected != before || next.LastMessage != "15" || next.BaselineBefore != "" {
				t.Fatal("invalid boundary progress")
			}
			if i == 0 {
				return crash
			}
			return nil
		})
		if (i == 0 && !errors.Is(err, crash)) || (i == 1 && !errors.Is(err, errMoreMessages)) {
			t.Fatal(err)
		}
	}
}

func TestBackwardBoundaryCannotAcceptForeignPeerOrNonadvancingPage(t *testing.T) {
	for _, response := range []object{
		{"messages": "invalid", "has_continue": false},
		{"messages": []any{}, "has_continue": "false"},
		{"messages": []any{object{"object_guid": "u0foreign", "message_id": "15", "time": 900, "type": "Text"}}, "has_continue": true},
		{"messages": []any{object{"message_id": "21", "time": 900, "type": "Text"}}, "has_continue": true},
		{"messages": []any{}, "has_continue": true},
	} {
		c, _ := newRPCFixture(t, func(string, object, bool) object { return response })
		c.startedAt = 1000
		cp := checkpoint{Version: 2, Account: "u0self", Peer: "u0peer", State: "100", BaselineBefore: "20"}
		err := c.recoverBaselinePage(context.Background(), pendingChat{GUID: "u0peer"}, checkpointJSON(cp), cp, func(context.Context, domains.EventBatch) error { t.Fatal("invalid page committed"); return nil })
		if err == nil || errors.Is(err, errMoreMessages) {
			t.Fatal("invalid page treated as progress")
		}
	}
}

func TestEmptyServiceArchiveWithOmittedFieldsStillDrainsNewMessages(t *testing.T) {
	for _, empty := range []object{{}, {"messages": nil}, {"messages": []any{}}, {"has_continue": false}, {"messages": []any{}, "has_continue": false}} {
		calls := 0
		c, _ := newRPCFixture(t, func(method string, input object, _ bool) object {
			if method != "getMessages" {
				t.Fatal("unexpected native request")
			}
			calls++
			if calls == 1 {
				if input.str("sort") != "FromMax" || input.str("max_id") != "20" {
					t.Fatal("wrong archive cursor")
				}
				return empty
			}
			if input.str("sort") != "FromMin" || input.str("min_id") != "1" {
				t.Fatal("new-message scan skipped its beginning")
			}
			// Like the reviewed Web client, absent has_continue means false.
			return object{"messages": []any{object{"message_id": "21", "time": 1001, "type": "Text", "text": "synthetic", "author_object_guid": "s0service"}}}
		})
		c.startedAt = 1000
		cp := checkpoint{Version: 2, Account: "u0self", Peer: "s0service", State: "100", BaselineBefore: "20"}
		raw := checkpointJSON(cp)
		messages := 0
		sink := c.enrichSink(func(_ context.Context, batch domains.EventBatch) error {
			for _, event := range batch.Events {
				if event.Type == "message" {
					messages++
					if event.MessageID != "21" {
						t.Fatal("wrong message")
					}
				}
			}
			if len(batch.Checkpoints) != 1 || batch.Checkpoints[0].Expected != raw {
				t.Fatal("checkpoint advanced without durable comparison")
			}
			raw = batch.Checkpoints[0].Next
			return nil
		})
		p := pendingChat{GUID: "s0service", LastMessage: "21", Backfill: true}
		err := c.recoverBaselinePage(context.Background(), p, raw, cp, sink)
		if !errors.Is(err, errMoreMessages) {
			t.Fatalf("empty archive blocked recovery: %v", err)
		}
		next, err := parseCheckpoint(raw, "u0self")
		if err != nil || !next.ScanFromStart || next.BaselineBefore != "" || next.State != "100" {
			t.Fatal("invalid archive boundary")
		}
		if err = c.recoverNewMessages(context.Background(), p, next, sink); err != nil {
			t.Fatal(err)
		}
		final, err := parseCheckpoint(raw, "u0self")
		if err != nil || final.LastMessage != "21" || final.ScanFromStart || calls != 2 || messages != 1 {
			t.Fatalf("wrong drain result: %+v messages=%d calls=%d", final, messages, calls)
		}
	}
}

func TestOldMediaDoesNotBlockInitialHistoryBoundary(t *testing.T) {
	for _, backfill := range []bool{false, true} {
		calls := 0
		c, _ := newRPCFixture(t, func(method string, _ object, _ bool) object {
			calls++
			if method != "getMessagesInterval" {
				t.Fatal("old archive unnecessarily scanned")
			}
			return object{"state": "100", "messages": []any{object{"message_id": "41", "time": 900, "type": "Image", "file_inline": object{"type": "Image", "file_id": "obsolete-reference"}}}}
		})
		c.startedAt = 1000
		raw := ""
		c.cfg.LoadCheckpoint = func(context.Context, string) (string, error) { return raw, nil }
		sink := c.enrichSink(func(_ context.Context, batch domains.EventBatch) error {
			for _, event := range batch.Events {
				if event.Type == "message" {
					t.Fatal("historical attachment imported")
				}
			}
			raw = batch.Checkpoints[0].Next
			return nil
		})
		_, err := c.recoverPeer(context.Background(), pendingChat{GUID: "c0channel", LastMessage: "41", Backfill: backfill}, sink)
		cp, parseErr := parseCheckpoint(raw, "u0self")
		if err != nil || parseErr != nil || cp.LastMessage != "41" || calls != 1 {
			t.Fatalf("old media blocked baseline: %v %v %+v", err, parseErr, cp)
		}
	}
}

func TestBackwardBoundaryDoesNotDecodeHistoricalMedia(t *testing.T) {
	c, _ := newRPCFixture(t, func(method string, _ object, _ bool) object {
		if method != "getMessages" {
			t.Fatal("wrong archive read")
		}
		return object{"messages": []any{object{"message_id": "15", "time": 900, "type": "Video", "file_inline": object{"type": "Video", "size": 1 << 30}}}, "has_continue": true}
	})
	c.startedAt = 1000
	cp := checkpoint{Version: 2, Account: "u0self", Peer: "c0channel", State: "100", BaselineBefore: "20"}
	before := checkpointJSON(cp)
	err := c.recoverBaselinePage(context.Background(), pendingChat{GUID: "c0channel"}, before, cp, func(_ context.Context, batch domains.EventBatch) error {
		if len(batch.Events) != 0 || batch.Checkpoints[0].Expected != before {
			t.Fatal("historical media published or cursor not compared")
		}
		next, err := parseCheckpoint(batch.Checkpoints[0].Next, "u0self")
		if err != nil || next.LastMessage != "15" || next.BaselineBefore != "" {
			t.Fatal("wrong history boundary")
		}
		return nil
	})
	if !errors.Is(err, errMoreMessages) {
		t.Fatalf("old media prevented new-message drain: %v", err)
	}
}
