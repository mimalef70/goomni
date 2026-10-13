package rubikameow

import (
	"context"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

func TestSenderEnrichmentFiltersOldIncomingAndOwnMessages(t *testing.T) {
	calls := 0
	c, _ := newRPCFixture(t, func(method string, input object, _ bool) object {
		calls++
		if method == "getContacts" {
			return object{"users": []any{}}
		}
		if method != "getUserInfo" || input.str("user_guid") != "u0peer" {
			t.Fatal("wrong profile identity")
		}
		return object{"user": object{"user_guid": "u0peer", "first_name": "سارا", "last_name": "احمدی"}}
	})
	c.startedAt = 1000
	batch := domains.EventBatch{Checkpoints: []domains.CheckpointTransition{{Scope: accountScope, Expected: "before", Next: "after"}}}
	for _, sender := range []string{"u0peer", "u0self"} {
		batch.Events = append(batch.Events, domains.Event{Type: "message", Time: time.Unix(999, 0), Message: &domains.Message{From: sender}})
	}
	for _, date := range []int64{1000, 1001} {
		batch.Events = append(batch.Events, domains.Event{Type: "message", Time: time.Unix(date, 0), Message: &domains.Message{From: "u0peer"}})
	}
	err := c.enrichSink(func(_ context.Context, b domains.EventBatch) error {
		if len(b.Events) != 2 || len(b.Checkpoints) != 1 {
			t.Fatal("old history or checkpoint error")
		}
		for _, event := range b.Events {
			if event.Message.SenderDisplayName != "سارا احمدی" || event.Message.SenderNameStatus != "available" {
				t.Fatal("name absent at persistence boundary")
			}
		}
		return nil
	})(context.Background(), batch)
	if err != nil || calls != 2 {
		t.Fatalf("enrichment: %v calls=%d", err, calls)
	}
}

func TestForeignProfileCannotBecomeSenderName(t *testing.T) {
	c, _ := newRPCFixture(t, func(string, object, bool) object {
		return object{"user": object{"user_guid": "u0foreign", "first_name": "Wrong"}}
	})
	err := c.enrichSink(func(_ context.Context, b domains.EventBatch) error {
		if b.Events[0].Message.SenderNameStatus != "unavailable" || b.Events[0].Message.SenderDisplayName != "" {
			t.Fatal("foreign label accepted")
		}
		return nil
	})(context.Background(), domains.EventBatch{Events: []domains.Event{{Message: &domains.Message{From: "u0peer"}}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryCurrentDoesNotHidePendingPeersOrRetainedGaps(t *testing.T) {
	c, _ := newRPCFixture(t, func(string, object, bool) object { return nil })
	c.setRecoveryStatus(checkpoint{})
	if c.Status().Recovery != "current" {
		t.Fatal("healthy cursor never becomes current")
	}
	c.setRecoveryStatus(checkpoint{Pending: []pendingChat{{GUID: "u0peer"}}})
	if c.Status().Recovery != "recovering" {
		t.Fatal("pending peer hidden")
	}
	c.setRecoveryStatus(checkpoint{Gap: true, GapReason: "message_state_expired"})
	if c.Status().Recovery != "gap_detected" {
		t.Fatal("retained gap hidden")
	}
	c.setRecoveryStatus(checkpoint{Gap: true, GapReason: "legacy_unclassified"})
	if c.Status().Recovery != "degraded" {
		t.Fatal("unclassified evidence hidden")
	}
}

func TestAddressBookNamesPrecedePublicProfileAndWarmOtherContacts(t *testing.T) {
	calls := 0
	c, _ := newRPCFixture(t, func(method string, _ object, _ bool) object {
		calls++
		if method != "getContacts" {
			t.Fatal("public profile preferred over saved local name")
		}
		return object{"users": []any{object{"user_guid": "u0peer", "first_name": "Saved local label"}, object{"user_guid": "u0next", "first_name": "Other local label"}}}
	})
	batch := domains.EventBatch{Events: []domains.Event{{Message: &domains.Message{From: "u0peer"}}, {Message: &domains.Message{From: "u0next"}}}}
	if err := c.enrichSink(func(_ context.Context, b domains.EventBatch) error {
		if b.Events[0].Message.SenderDisplayName != "Saved local label" || b.Events[1].Message.SenderDisplayName != "Other local label" {
			t.Fatal("native contact labels missing")
		}
		return nil
	})(context.Background(), batch); err != nil || calls != 1 {
		t.Fatalf("contact lookup/cache: %v calls=%d", err, calls)
	}
}

func TestContactReadFailureStillAllowsProfileNameAndDurableMessage(t *testing.T) {
	calls := 0
	c, _ := newRPCFixture(t, func(method string, _ object, _ bool) object {
		calls++
		if method == "getContacts" {
			return nil
		}
		return object{"user": object{"user_guid": "u0peer", "first_name": "Profile fallback"}}
	})
	if err := c.enrichSink(func(_ context.Context, b domains.EventBatch) error {
		if b.Events[0].Message.SenderDisplayName != "Profile fallback" {
			t.Fatal("optional contact read blocked profile fallback")
		}
		return nil
	})(context.Background(), domains.EventBatch{Events: []domains.Event{{Message: &domains.Message{From: "u0peer"}}}}); err != nil || calls != 2 {
		t.Fatal("message lost on contact failure", err)
	}
}

func TestLaterContactPageAndEmptyAliasFallBackSafely(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "later contact page", true: "empty saved name"}[empty], func(t *testing.T) {
			calls := 0
			c, _ := newRPCFixture(t, func(method string, input object, _ bool) object {
				calls++
				if method == "getUserInfo" {
					if !empty {
						t.Fatal("later-page contact was skipped")
					}
					return object{"user": object{"user_guid": "u0peer", "first_name": "Public profile"}}
				}
				if input.str("start_id") == "" {
					return object{"users": []any{object{"user_guid": "u0other", "first_name": "Other"}}, "has_continue": true, "next_start_id": "next-page"}
				}
				if input.str("start_id") != "next-page" {
					t.Fatal("unverified pagination boundary")
				}
				name := "Saved local label"
				if empty {
					name = ""
				}
				return object{"users": []any{object{"user_guid": "u0peer", "first_name": name, "username": "fallback-username"}}, "has_continue": false}
			})
			name, status := c.names.Resolve(context.Background(), "u0self", "u0peer", func(ctx context.Context) (string, error) { return c.lookupSenderName(ctx, "u0self", "u0peer") })
			expected := "Saved local label"
			wantCalls := 2
			if empty {
				expected = "Public profile"
				wantCalls = 3
			}
			if name != expected || status != "available" || calls != wantCalls {
				t.Fatalf("contact paging/empty alias: %s %s %d", name, status, calls)
			}
		})
	}
}
