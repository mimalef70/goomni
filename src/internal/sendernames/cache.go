// Package sendernames bounds account-scoped display-name enrichment before
// durable event acceptance. A missing name never changes message identity.
package sendernames

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

type entry struct {
	local   bool
	name    string
	expires time.Time
}

type Cache struct {
	mu         sync.Mutex
	entries    map[string]entry
	nextLookup time.Time
}

func DisplayName(first, last, username string) string {
	name := strings.TrimSpace(strings.TrimSpace(first) + " " + strings.TrimSpace(last))
	if name == "" {
		name = strings.TrimSpace(username)
	}
	if !utf8.ValidString(name) || len(name) > 4096 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return ""
	}
	return name
}

func (c *Cache) Remember(account, sender, name string) {
	if account == "" || sender == "" || name == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store(account+"|"+sender, name, false)
}

// RememberContact retains an account-owned address-book label ahead of a
// profile observation for the same sender. Its TTL still permits later refresh.
func (c *Cache) RememberContact(account, sender, name string) {
	if account == "" || sender == "" || name == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store(account+"|"+sender, name, true)
}

func (c *Cache) store(key, name string, local bool) {
	if c.entries == nil {
		c.entries = make(map[string]entry)
	}
	now := time.Now()
	if previous := c.entries[key]; now.Before(previous.expires) && ((name == "" && previous.name != "") || (previous.local && !local)) {
		return
	}
	if _, exists := c.entries[key]; !exists && len(c.entries) >= 4096 {
		for old := range c.entries {
			delete(c.entries, old)
			break
		}
	}
	ttl := 15 * time.Minute
	if name == "" {
		ttl = 30 * time.Second
	}
	c.entries[key] = entry{name: name, expires: now.Add(ttl), local: local}
}

func (c *Cache) Resolve(ctx context.Context, account, sender string, load func(context.Context) (string, error)) (string, string) {
	key := account + "|" + sender
	waiting, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	for {
		c.mu.Lock()
		now := time.Now()
		if cached, ok := c.entries[key]; ok && now.Before(cached.expires) {
			c.mu.Unlock()
			if cached.name != "" {
				return cached.name, "available"
			}
			return "", "unavailable"
		}
		if waiting.Err() != nil {
			c.mu.Unlock()
			return "", "unavailable"
		}
		delay := c.nextLookup.Sub(now)
		if delay <= 0 {
			c.nextLookup = now.Add(time.Second)
			c.mu.Unlock()
			break
		}
		c.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-waiting.Done():
			timer.Stop()
			return "", "unavailable"
		}
	}
	lookup, stop := context.WithTimeout(waiting, 500*time.Millisecond)
	defer stop()
	name, _ := load(lookup)
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() == nil {
		c.store(key, name, false)
	}
	if cached := c.entries[key]; time.Now().Before(cached.expires) && cached.name != "" {
		return cached.name, "available"
	}
	return "", "unavailable"
}
