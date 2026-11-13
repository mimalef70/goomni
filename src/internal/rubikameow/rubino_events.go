package rubikameow

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

// rubinoScope holds the Rubino activity watermark, separate from messenger
// checkpoints so a Rubino failure never stalls message recovery.
const rubinoScope = "rubino.activity"

const (
	rubinoPollInterval = 30 * time.Second
	rubinoIdleRetry    = time.Hour
	rubinoMaxPages     = 5
	rubinoPageSize     = 20
	rubinoCommentPages = 3
)

type rubinoCheckpoint struct {
	Version int    `json:"version"`
	Account string `json:"account"`
	Profile string `json:"profile"`
	LastID  string `json:"last_id,omitempty"`
	Gap     bool   `json:"gap,omitempty"`
}

// pollRubino emits Rubino activity for the account's default profile until
// ctx ends. Comments become "rubino.comment" events with their text; other
// activity (likes, follows, mentions) becomes "rubino.activity".
func (c *Client) pollRubino(ctx context.Context, sink domains.BatchSink) {
	timer := time.NewTimer(pollJitter(5 * time.Second))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		delay := rubinoPollInterval
		if err := c.rubinoPollOnce(ctx, sink); err != nil {
			delay = 2 * rubinoPollInterval
			var de *domains.Error
			if asDomainError(err, &de) && (de.Code == "RUBINO_PROFILE_REQUIRED" || de.Code == "AUTH_REQUIRED") {
				delay = rubinoIdleRetry
			}
		}
		timer.Reset(pollJitter(delay))
	}
}

func (c *Client) rubinoPollOnce(ctx context.Context, sink domains.BatchSink) error {
	if c.cfg.PollPermit != nil {
		release, err := c.cfg.PollPermit(ctx)
		if err != nil {
			return err
		}
		defer release()
	}
	profile, err := c.cachedRubinoProfile(ctx)
	if err != nil {
		return err
	}
	c.mu.RLock()
	account := c.session.UserID
	c.mu.RUnlock()
	raw, err := c.cfg.LoadCheckpoint(ctx, rubinoScope)
	if err != nil {
		return err
	}
	var cp rubinoCheckpoint
	if raw != "" {
		if json.Unmarshal([]byte(raw), &cp) != nil || cp.Version != 1 || cp.Account != account {
			return domains.E("INVALID_CHECKPOINT", "Rubino checkpoint belongs to another account", 500)
		}
	}
	records, reached, err := c.newRubinoRecords(ctx, profile, cp.LastID)
	if err != nil {
		return err
	}
	next := rubinoCheckpoint{Version: 1, Account: account, Profile: profile, LastID: cp.LastID}
	if len(records) > 0 {
		next.LastID = records[0].str("id")
	}
	// First observation is a baseline: existing activity is not replayed as new.
	baseline := raw == "" || cp.Profile != profile
	var events []domains.Event
	if !baseline {
		next.Gap = !reached
		for i := len(records) - 1; i >= 0; i-- {
			events = append(events, c.rubinoEvent(ctx, account, profile, records[i]))
		}
	}
	nextRaw, _ := json.Marshal(next)
	if raw != "" && string(nextRaw) == raw && len(events) == 0 {
		return nil
	}
	return sink(ctx, domains.EventBatch{Events: events, Checkpoints: []domains.CheckpointTransition{{Scope: rubinoScope, Expected: raw, Next: string(nextRaw)}}})
}

// newRubinoRecords returns activity newer than lastID, newest first, and
// whether lastID was reached within the bounded page budget.
func (c *Client) newRubinoRecords(ctx context.Context, profile, lastID string) ([]object, bool, error) {
	var out []object
	maxID := ""
	for page := 0; page < rubinoMaxPages; page++ {
		data := object{"profile_id": profile, "limit": rubinoPageSize, "sort": "FromMax"}
		if maxID != "" {
			data["max_id"] = maxID
		}
		result, err := c.rubino(ctx, "getNewEvents", data, false)
		if err != nil {
			return nil, false, err
		}
		records := asObjects(result["records"])
		for _, record := range records {
			id := record.str("id")
			if !validRubinoID(id) {
				continue
			}
			if lastID != "" && !rubinoIDNewer(id, lastID) {
				return out, true, nil
			}
			out = append(out, record)
		}
		if lastID == "" || len(records) < rubinoPageSize {
			return out, true, nil
		}
		maxID = records[len(records)-1].str("id")
	}
	return out, false, nil
}

// rubinoIDNewer compares Rubino activity IDs, which are decimal timestamps.
func rubinoIDNewer(a, b string) bool {
	if len(a) != len(b) {
		return len(a) > len(b)
	}
	return a > b
}

func (c *Client) rubinoEvent(ctx context.Context, account, profile string, record object) domains.Event {
	projected := projectRubinoEvent(record)
	kind := rubinoEventKind(record.str("model"))
	at := time.Now().UTC()
	if seconds, err := strconv.ParseInt(fmtNumber(record["create_date"]), 10, 64); err == nil && seconds > 0 {
		at = time.Unix(seconds, 0).UTC()
	}
	event := domains.Event{Provider: domains.ProviderRubika, ID: eventHash("rubino|" + account + "|" + profile + "|" + record.str("id")), Type: kind, Direction: "incoming", Time: at}
	if kind == "rubino.comment" || kind == "rubino.mention" {
		if comment := c.findRubinoComment(ctx, profile, record.str("post_id"), record.str("post_profile_id"), record.str("comment_id")); comment != nil {
			projected["comment"] = projectRubinoComment(comment)
			projected["content_status"] = "available"
		} else {
			projected["content_status"] = "unavailable"
		}
	}
	if owners := asObjects(record["owners"]); len(owners) > 0 && validRubinoID(owners[0].str("id")) {
		if peer, err := c.rubinoProfileChat(ctx, profile, owners[0].str("id")); err == nil {
			event.Peer = peer
			event.SenderID = peer.ID
		}
	}
	event.Payload, _ = json.Marshal(projected)
	return event
}

// rubinoEventKind classifies observed notification models. "CommentPost" is a
// new comment (top-level or a thread reply); "TagComment" is a comment that
// mentions the profile and accompanies the CommentPost for the same comment.
func rubinoEventKind(model string) string {
	lower := strings.ToLower(model)
	switch {
	case strings.HasPrefix(lower, "tag") && strings.Contains(lower, "comment"):
		return "rubino.mention"
	case strings.HasPrefix(lower, "like"):
		return "rubino.activity"
	case strings.Contains(lower, "comment"):
		return "rubino.comment"
	}
	return "rubino.activity"
}

// rubinoReplyLookups bounds thread searches for one notification.
const rubinoReplyLookups = 10

// findRubinoComment locates a comment's text on its post within a page budget,
// searching thread replies of commented items because reply notifications do
// not identify the parent comment.
func (c *Client) findRubinoComment(ctx context.Context, profile, postID, postProfile, commentID string) object {
	if !validRubinoID(postID) || !validRubinoID(postProfile) || !validRubinoID(commentID) {
		return nil
	}
	lookups := 0
	maxID := ""
	for page := 0; page < rubinoCommentPages; page++ {
		data := object{"profile_id": profile, "post_id": postID, "post_profile_id": postProfile, "limit": rubinoPageSize, "sort": "FromMax"}
		if maxID != "" {
			data["max_id"] = maxID
		}
		result, err := c.rubino(ctx, "getComments", data, false)
		if err != nil {
			return nil
		}
		comments := asObjects(result["comments"])
		for _, comment := range comments {
			if comment.str("id") == commentID {
				return comment
			}
		}
		for _, parent := range comments {
			if fmtNumber(parent["reply_count"]) == "0" || fmtNumber(parent["reply_count"]) == "" || lookups >= rubinoReplyLookups {
				continue
			}
			lookups++
			replies, err := c.rubino(ctx, "getCommentReplies", object{"profile_id": profile, "post_id": postID, "post_profile_id": postProfile, "comment_id": parent.str("id"), "limit": rubinoPageSize, "sort": "FromMax"}, false)
			if err != nil {
				continue
			}
			for _, reply := range asObjects(replies["comments"]) {
				if reply.str("id") == commentID {
					if reply.str("parent_id") == "" {
						reply["parent_id"] = parent.str("id")
					}
					return reply
				}
			}
		}
		if len(comments) < rubinoPageSize {
			return nil
		}
		maxID = comments[len(comments)-1].str("id")
	}
	return nil
}

func fmtNumber(v any) string {
	switch n := v.(type) {
	case json.Number:
		return n.String()
	case string:
		return n
	}
	return ""
}
