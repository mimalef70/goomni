package eitaameow

import (
	"context"
	"strconv"

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
			if message := event.Message; message != nil && event.Peer.Type == "user" && (message.From == account || message.From == event.Peer.ID) && (protocolRules{}).ValidatePeer(domains.Peer{Type: "user", ID: message.From}) == nil {
				message.SenderDisplayName, message.SenderNameStatus = c.names.Resolve(ctx, account, message.From, func(ctx context.Context) (string, error) {
					user, err := c.inputUser(ctx, message.From)
					if err != nil {
						return "", err
					}
					response, err := c.invoke(ctx, "users.getFullUser", object{"id": user}, false, false)
					if err != nil {
						return "", err
					}
					profile := asObject(response["user"])
					if response.str("_") != "userFull" || strconv.FormatInt(profile.num("id"), 10) != message.From {
						return "", protocolError()
					}
					return sendernames.DisplayName(profile.str("first_name"), profile.str("last_name"), profile.str("username")), nil
				})
			}
			events = append(events, event)
		}
		batch.Events = events
		c.mu.RLock()
		unchanged := c.session.UserID == account
		c.mu.RUnlock()
		if !unchanged {
			return domains.E("ACCOUNT_CHANGED", "Eitaa account changed during enrichment", 409)
		}
		return sink(ctx, batch)
	}
}
