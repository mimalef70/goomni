package eitaameow

import (
	"context"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

func TestSenderEnrichmentAndInitialHistoryBoundary(t *testing.T) {
	calls := 0
	c, _ := nativeFixture(t, func(method string, input object) (object, int) {
		calls++
		if method != "users.getFullUser" || asObject(input["id"]).num("access_hash") != 888 {
			t.Fatal("profile reference is not account owned")
		}
		return object{"_": "userFull", "user": object{"_": "user", "id": 7, "first_name": "علی", "last_name": "رضایی"}, "about": "", "common_chats_count": 0, "settings": object{"_": "peerSettings"}, "notify_settings": object{"_": "peerNotifySettings"}}, 200
	})
	c.session.UserID, c.session.Token = "42", "synthetic"
	c.session.Peers = map[string]object{"user:7": {"_": "inputPeerUser", "user_id": int64(7), "access_hash": int64(888)}}
	c.startedAt = 1000
	batch := domains.EventBatch{Checkpoints: []domains.CheckpointTransition{{Scope: accountScope, Expected: "before", Next: "after"}}}
	for _, date := range []int64{999, 1000, 1001} {
		batch.Events = append(batch.Events, domains.Event{Peer: domains.Peer{Type: "user", ID: "7"}, Type: "message", Time: time.Unix(date, 0), Message: &domains.Message{From: "7", SenderNameStatus: "unavailable"}})
	}
	err := c.enrichSink(func(_ context.Context, b domains.EventBatch) error {
		if len(b.Events) != 2 || len(b.Checkpoints) != 1 || b.Checkpoints[0].Expected != "before" {
			t.Fatal("initial history accepted or checkpoint lost")
		}
		for _, event := range b.Events {
			if event.Message.SenderDisplayName != "علی رضایی" || event.Message.SenderNameStatus != "available" {
				t.Fatal("name not completed before durable acceptance")
			}
		}
		return nil
	})(context.Background(), batch)
	if err != nil || calls != 1 {
		t.Fatalf("lookup/cache failure: %v calls=%d", err, calls)
	}
}

func TestBulkEntityNamesDoNotRequireAnotherProfileRead(t *testing.T) {
	c, _ := nativeFixture(t, func(string, object) (object, int) { t.Fatal("unnecessary profile read"); return nil, 500 })
	c.session.UserID, c.session.Token = "42", "synthetic"
	if err := c.rememberEntities(context.Background(), object{"users": []any{object{"_": "user", "id": 7, "first_name": "مخاطب"}}}); err != nil {
		t.Fatal(err)
	}
	err := c.enrichSink(func(_ context.Context, b domains.EventBatch) error {
		if b.Events[0].Message.SenderDisplayName != "مخاطب" {
			t.Fatal("bulk name ignored")
		}
		return nil
	})(context.Background(), domains.EventBatch{Events: []domains.Event{{Peer: domains.Peer{Type: "user", ID: "7"}, Message: &domains.Message{From: "7"}}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryCurrentOnlyAfterKnownWorkAndCoverageClear(t *testing.T) {
	c, _ := nativeFixture(t, func(string, object) (object, int) { return nil, 500 })
	c.setRecoveryStatus(checkpoint{})
	if c.Status().Recovery != "current" {
		t.Fatal("idle healthy connection never becomes current")
	}
	c.setRecoveryStatus(checkpoint{More: true})
	if c.Status().Recovery != "recovering" {
		t.Fatal("unfinished page hidden")
	}
	c.setRecoveryStatus(checkpoint{Gap: true, GapReason: "difference_too_long"})
	if c.Status().Recovery != "gap_detected" {
		t.Fatal("known gap hidden")
	}
	c.setRecoveryStatus(checkpoint{Unsupported: true})
	if c.Status().Recovery != "degraded" {
		t.Fatal("unsupported coverage hidden")
	}
}

func TestChannelSenderNamespaceIsNotLookedUpAsUnrelatedUser(t *testing.T) {
	c, _ := nativeFixture(t, func(string, object) (object, int) { t.Fatal("channel author queried as a user"); return nil, 500 })
	c.session.UserID = "42"
	err := c.enrichSink(func(_ context.Context, b domains.EventBatch) error {
		if b.Events[0].Message.SenderDisplayName != "" {
			t.Fatal("wrong sender namespace label")
		}
		return nil
	})(context.Background(), domains.EventBatch{Events: []domains.Event{{Peer: domains.Peer{Type: "channel", ID: "7"}, Message: &domains.Message{From: "7"}}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAccountContactLabelSurvivesLaterProfileMetadata(t *testing.T) {
	c, _ := nativeFixture(t, func(string, object) (object, int) { t.Fatal("contact name unnecessarily fetched"); return nil, 500 })
	c.session.UserID = "42"
	for _, response := range []object{
		{"_": "contacts.contacts", "users": []any{object{"_": "user", "id": 7, "first_name": "Saved local label"}}},
		{"users": []any{object{"_": "user", "id": 7, "first_name": "Public profile"}}},
	} {
		if err := c.rememberEntities(context.Background(), response); err != nil {
			t.Fatal(err)
		}
	}
	err := c.enrichSink(func(_ context.Context, b domains.EventBatch) error {
		if b.Events[0].Message.SenderDisplayName != "Saved local label" {
			t.Fatal("public profile replaced local label")
		}
		return nil
	})(context.Background(), domains.EventBatch{Events: []domains.Event{{Peer: domains.Peer{Type: "user", ID: "7"}, Message: &domains.Message{From: "7"}}}})
	if err != nil {
		t.Fatal(err)
	}
}
