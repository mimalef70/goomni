package rubikameow

import (
	"context"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/sendernames"
)

func (c *Client) enrichSink(sink domains.BatchSink) domains.BatchSink {
	return func(ctx context.Context, batch domains.EventBatch) error {
		c.mu.RLock()
		account, startedAt := c.session.UserID, c.startedAt
		c.mu.RUnlock()
		events := make([]domains.Event, 0, len(batch.Events))
		for _, event := range batch.Events {
			if event.Type == "message" && startedAt > 0 && event.Time.Unix() < startedAt {
				continue
			}
			if message := event.Message; message != nil && peerFromGUID(message.From).Type == "user" && (protocolRules{}).ValidateSenderID(message.From) {
				message.SenderDisplayName, message.SenderNameStatus = c.names.Resolve(ctx, account, message.From, func(ctx context.Context) (string, error) {
					return c.lookupSenderName(ctx, account, message.From)
				})
			}
			events = append(events, event)
		}
		batch.Events = events
		c.mu.RLock()
		unchanged := c.session.UserID == account
		c.mu.RUnlock()
		if !unchanged {
			return domains.E("ACCOUNT_CHANGED", "Rubika account changed during enrichment", 409)
		}
		return sink(ctx, batch)
	}
}

// Contacts and public profiles can carry different names. Resolve the native
// account-owned contact label first, within the same bounded lookup budget.
func (c *Client) lookupSenderName(ctx context.Context, account, sender string) (string, error) {
	c.mu.Lock()
	refresh := !time.Now().Before(c.senderContactsAfter)
	if refresh {
		c.senderContactsAfter = time.Now().Add(30 * time.Second)
	}
	c.mu.Unlock()
	if refresh {
		cursor := ""
		for page := 0; page < 3; page++ {
			input := object{}
			if cursor != "" {
				input["start_id"] = cursor
			}
			contacts, err := c.invoke(ctx, "getContacts", input, false, "")
			if err != nil {
				break
			}
			rows, ok := contacts["users"].([]any)
			if !ok || len(rows) > 1000 {
				break
			}
			selected := ""
			for _, value := range rows {
				user := asObject(value)
				id := user.str("user_guid")
				if peerFromGUID(id).Type != "user" || !(protocolRules{}).ValidateSenderID(id) {
					continue
				}
				name := sendernames.DisplayName(user.str("first_name"), user.str("last_name"), "")
				c.names.RememberContact(account, id, name)
				if id == sender {
					selected = name
				}
			}
			if selected != "" {
				return selected, nil
			}
			more, _ := contacts["has_continue"].(bool)
			next := contacts.str("next_start_id")
			if !more || len(rows) == 0 || !domains.ValidOpaqueID(next) || next == cursor {
				break
			}
			cursor = next
		}
	}
	response, err := c.invoke(ctx, "getUserInfo", object{"user_guid": sender}, false, "")
	if err != nil {
		return "", err
	}
	profile := asObject(response["user"])
	if profile.str("user_guid") != sender {
		return "", protocolError()
	}
	return sendernames.DisplayName(profile.str("first_name"), profile.str("last_name"), profile.str("username")), nil
}
