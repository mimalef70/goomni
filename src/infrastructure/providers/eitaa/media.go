package eitaa

import (
	"strconv"
	"strings"

	"github.com/mimalef70/goomni/src/domains"
)

func (Contract) ValidateMediaRevision(peer domains.Peer, kind string, revision *domains.MediaRevision) error {
	if revision == nil {
		return nil
	}
	invalid := func() error {
		return domains.E("INVALID_MEDIA_REVISION", "attachment ordering evidence is invalid for this provider and peer", 502)
	}
	if revision.Sequence <= 0 || revision.Sequence > 2147483647 || len(revision.Scope) > 256 {
		return invalid()
	}
	wireID := peer.ID
	supergroup := peer.Type == "group" && strings.HasPrefix(peer.ID, "channel_")
	if supergroup {
		wireID = strings.TrimPrefix(peer.ID, "channel_")
	}
	id, err := strconv.ParseInt(wireID, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != wireID || peer.AccessHash != "" {
		return invalid()
	}
	if kind != "message" && kind != "message.edited" && kind != "message.deleted" {
		return invalid()
	}
	switch {
	case revision.Scope == "account":
		if (peer.Type != "user" && peer.Type != "group") || supergroup {
			return invalid()
		}
	case strings.HasPrefix(revision.Scope, "channel:"):
		if (peer.Type != "channel" && !supergroup) || revision.Scope != "channel:"+wireID {
			return invalid()
		}
	default:
		return invalid()
	}
	return nil
}
