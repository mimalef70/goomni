package sendernames

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSafeNamesPreserveUnicodeAndRejectControls(t *testing.T) {
	if DisplayName(" علی ", "رضایی", "ignored") != "علی رضایی" || DisplayName("", "", " username ") != "username" {
		t.Fatal("display name lost")
	}
	for _, name := range []string{"unsafe\nname", "unsafe\x00name", string([]byte{255})} {
		if DisplayName(name, "", "") != "" {
			t.Fatal("unsafe name accepted")
		}
	}
}

func TestCacheScopesNamesAndPreservesGoodBulkObservation(t *testing.T) {
	var cache Cache
	cache.Remember("account-a", "7", "علی")
	cache.Remember("account-a", "7", "")
	load := func(context.Context) (string, error) { t.Fatal("cached name fetched again"); return "", nil }
	if name, status := cache.Resolve(context.Background(), "account-a", "7", load); name != "علی" || status != "available" {
		t.Fatal("cached name lost")
	}
	if name, status := cache.Resolve(context.Background(), "account-b", "7", func(context.Context) (string, error) { return "سارا", nil }); name != "سارا" || status != "available" {
		t.Fatal("account scope lost")
	}
	if name, _ := cache.Resolve(context.Background(), "account-a", "7", load); name != "علی" {
		t.Fatal("another account replaced label")
	}
}

func TestTwoColdSendersBothReceiveBoundedLookup(t *testing.T) {
	var cache Cache
	var calls atomic.Int32
	var wg sync.WaitGroup
	for _, id := range []string{"7", "8"} {
		wg.Go(func() {
			name, status := cache.Resolve(context.Background(), "self", id, func(context.Context) (string, error) { calls.Add(1); return id, nil })
			if name != id || status != "available" {
				t.Errorf("cold sender %s lost its slot", id)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatal("lookup skipped")
	}
}

func TestFailedLookupIsBoundedAndNegativelyCached(t *testing.T) {
	var cache Cache
	started := time.Now()
	_, status := cache.Resolve(context.Background(), "self", "7", func(ctx context.Context) (string, error) { <-ctx.Done(); return "", ctx.Err() })
	if status != "unavailable" || time.Since(started) > time.Second {
		t.Fatal("lookup exceeded its budget")
	}
	_, status = cache.Resolve(context.Background(), "self", "7", func(context.Context) (string, error) {
		t.Fatal("failed lookup was not cached")
		return "", errors.New("unreachable")
	})
	if status != "unavailable" {
		t.Fatal("unavailable profile became available")
	}
}

func TestAddressBookNameTakesPriorityUntilItsBoundedExpiry(t *testing.T) {
	var c Cache
	c.Remember("self", "7", "Profile")
	c.RememberContact("self", "7", "Local label")
	c.Remember("self", "7", "New profile")
	c.RememberContact("self", "7", "")
	name, status := c.Resolve(context.Background(), "self", "7", func(context.Context) (string, error) { t.Fatal("cached contact refetched"); return "", nil })
	if name != "Local label" || status != "available" {
		t.Fatal("profile replaced native contact alias")
	}
	c.mu.Lock()
	v := c.entries["self|7"]
	v.expires = time.Now().Add(-time.Second)
	c.entries["self|7"] = v
	c.mu.Unlock()
	name, status = c.Resolve(context.Background(), "self", "7", func(context.Context) (string, error) { return "Current profile", nil })
	if name != "Current profile" || status != "available" {
		t.Fatal("expired contact label did not refresh")
	}
}
