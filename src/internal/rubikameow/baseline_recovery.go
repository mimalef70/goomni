package rubikameow

import (
	"context"
	"strconv"

	"github.com/mimalef70/goomni/src/domains"
)

// Locate a new peer's activation boundary with observed native IDs. Commit one
// bounded backward page at a time, then drain forward in chronological ID order.
// Existing new-message watermarks never enter this path or move backwards.
func (c *Client) recoverBaselinePage(ctx context.Context, p pendingChat, expected string, cp checkpoint, sink domains.BatchSink) error {
	o, err := c.invoke(ctx, "getMessages", object{"object_guid": p.GUID, "sort": "FromMax", "max_id": cp.BaselineBefore, "limit": 100}, false, "")
	if err != nil {
		return err
	}
	messages, hasMore, err := messagePage(o)
	if err != nil {
		return err
	}
	c.mu.RLock()
	startedAt := c.startedAt
	c.mu.RUnlock()
	maximum, _ := strconv.ParseInt(cp.BaselineBefore, 10, 64)
	minimum, beforeStart := int64(0), int64(0)
	for _, value := range messages {
		message := asObject(value)
		id := message.str("message_id")
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || !validMessageID(id) || n > maximum {
			return protocolError()
		}
		timestamp := message.num("time")
		if timestamp <= 0 {
			return protocolError()
		}
		if guid := message.str("object_guid"); guid != "" && guid != p.GUID {
			return protocolError()
		}
		if minimum == 0 || n < minimum {
			minimum = n
		}
		if startedAt > 0 && timestamp < startedAt {
			beforeStart = max(beforeStart, n)
		}
	}
	next := cp
	switch {
	case beforeStart > 0:
		next.LastMessage = strconv.FormatInt(beforeStart, 10)
		next.BaselineBefore = ""
	case !hasMore || minimum == 1:
		next.ScanFromStart = true
		next.BaselineBefore = ""
	case minimum > 1:
		next.BaselineBefore = strconv.FormatInt(minimum-1, 10)
	default:
		return protocolError()
	}
	if err := sink(ctx, domains.EventBatch{Checkpoints: []domains.CheckpointTransition{{Scope: "rubika.messages:" + p.GUID, Expected: expected, Next: checkpointJSON(next)}}}); err != nil {
		return err
	}
	return errMoreMessages
}
