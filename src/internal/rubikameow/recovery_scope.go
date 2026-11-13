package rubikameow

import (
	"context"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

func (c *Client) accountCheckpointScope() string {
	if c.cfg.DirectMessagesOnly {
		return "rubika.direct.chats"
	}
	return accountScope
}

// Keep the original activation time and account cursor when narrowing an
// existing connection. User message checkpoints remain shared, so switching
// modes never resets an accepted message watermark or skips outage messages.
// The independent account checkpoint excludes group/channel retry and gap state.
func (c *Client) loadRecoveryCheckpoint(ctx context.Context, account string, sink domains.BatchSink) (string, checkpoint, error) {
	scope := c.accountCheckpointScope()
	raw, err := c.cfg.LoadCheckpoint(ctx, scope)
	if err != nil {
		return "", checkpoint{}, err
	}
	initial := raw
	if raw == "" {
		previousScope := "rubika.direct.chats"
		if c.cfg.DirectMessagesOnly {
			previousScope = accountScope
		}
		initial, err = c.cfg.LoadCheckpoint(ctx, previousScope)
		if err != nil {
			return "", checkpoint{}, err
		}
	}
	cp, err := parseCheckpoint(initial, account)
	if err != nil {
		return "", cp, err
	}
	if cp.Peer != "" {
		return "", cp, domains.E("INVALID_CHECKPOINT", "account checkpoint contains a peer scope", 500)
	}
	if raw != "" {
		return raw, cp, nil
	}
	if initial == "" {
		cp.StartedAt = time.Now().UTC().Unix()
	} else {
		cp.Pending = c.recoveryPeers(cp.Pending)
		// Rediscover user peers without advancing the saved account cursor.
		// Their own durable checkpoints restore any real direct-message gaps.
		cp.Listing, cp.RefreshPeers = cp.State == "", cp.State != ""
		cp.Page = ""
		if c.cfg.DirectMessagesOnly && cp.GapReason == "message_state_expired" {
			// Inspect user checkpoints before dropping a group-only aggregate,
			// including users absent from the current chat listing.
			if c.cfg.LoadCheckpointPage == nil {
				cp.GapReason = "legacy_unclassified"
			} else {
				cp.Gap, cp.GapReason = false, ""
				after := ""
				for {
					page, err := c.cfg.LoadCheckpointPage(ctx, "rubika.messages:u0", after)
					if err != nil {
						return "", cp, err
					}
					if len(page) > 100 {
						return "", cp, domains.E("INVALID_CHECKPOINT", "user checkpoint page exceeds its bound", 500)
					}
					if len(page) == 0 {
						break
					}
					nextAfter := after
					for scope, value := range page {
						peer, err := parseCheckpoint(value, account)
						if err != nil {
							return "", cp, err
						}
						if !(protocolRules{}).ValidateUserID(peer.Peer) || scope != "rubika.messages:"+peer.Peer || scope <= after {
							return "", cp, domains.E("INVALID_CHECKPOINT", "user checkpoint page has an invalid scope", 500)
						}
						if peer.Gap && (!cp.Gap || peer.GapReason != "legacy_unclassified") {
							cp.Gap, cp.GapReason = true, peer.GapReason
						}
						nextAfter = max(nextAfter, scope)
					}
					after = nextAfter
				}
			}
		}
	}
	next := checkpointJSON(cp)
	if err := sink(ctx, domains.EventBatch{Checkpoints: []domains.CheckpointTransition{{Scope: scope, Expected: raw, Next: next}}}); err != nil {
		return "", cp, err
	}
	return next, cp, nil
}

func (c *Client) recoveryPeers(peers []pendingChat) []pendingChat {
	result := make([]pendingChat, 0, len(peers))
	for _, peer := range peers {
		if !c.cfg.DirectMessagesOnly || (protocolRules{}).ValidateUserID(peer.GUID) {
			result = append(result, peer)
		}
	}
	return result
}

func (c *Client) excludedPeer(guid string) bool {
	if !c.cfg.DirectMessagesOnly {
		return false
	}
	switch peerFromGUID(guid).Type {
	case "group", "channel", "bot", "service":
		return true
	}
	// Unknown namespaces remain audit diagnostics, without affecting direct
	// coverage until the provider can bind them to a user conversation.
	return false
}

// Reject out-of-scope peers and known pre-attachment content before projection:
// an old attachment or a channel payload must not fail direct-message recovery.
func (c *Client) skipMessageUpdate(guid string, update object) bool {
	if !c.cfg.DirectMessagesOnly {
		return false
	}
	if c.excludedPeer(guid) {
		return true
	}
	c.mu.RLock()
	startedAt := c.startedAt
	c.mu.RUnlock()
	timestamp := asObject(update["message"]).num("time")
	return startedAt > 0 && timestamp > 0 && timestamp < startedAt
}
