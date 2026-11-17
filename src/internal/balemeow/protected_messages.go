package balemeow

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
)

// Observed in the official web client on 2026-11-17 (ProtectedMessage.tsx and
// the Ramz module): the account owner's PIN is checked with services_type BOT
// and the returned token authorizes exactly one FetchProtectedMessage call.
const (
	ramzService            = "bale.ramz.v1.Ramz"
	ramzServicesTypeBot    = 1
	ramzTokenMetadataKey   = "bale_ramz_token"
	maxRamzTokenBytes      = 8 << 10
	protectedMessageMethod = "FetchProtectedMessage"
)

// unlockProtectedMessage performs one caller-initiated PIN check. The PIN and
// the provider token are used only for these two requests: they are never
// stored, retried, logged, returned or added to events. Unlocked content is
// returned to the caller only and is not written to history or webhooks.
func (c *Client) unlockProtectedMessage(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Peer      domains.Peer `json:"peer"`
		MessageID string       `json:"message_id"`
		Date      string       `json:"date"`
		PIN       string       `json:"pin"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return nil, boundedError("INVALID_REQUEST", "invalid protected message unlock body", 400)
	}
	if p.Peer.Type != "user" {
		return nil, boundedError("INVALID_PEER", "protected messages are supported only in private user chats", 400)
	}
	if len(p.PIN) != 6 || !isASCIIDigits(p.PIN) {
		return nil, boundedError("INVALID_REQUEST", "pin must be exactly six ASCII digits", 400)
	}
	rid, err := messageID(p.MessageID)
	if err != nil {
		return nil, boundedError("INVALID_REQUEST", "message_id must be a nonzero signed int64 string", 400)
	}
	date, err := positiveID(p.Date)
	if err != nil {
		return nil, boundedError("INVALID_REQUEST", "date is the protected message's millisecond date", 400)
	}
	peer, err := c.messagePeer(ctx, p.Peer)
	if err != nil {
		return nil, err
	}

	// Attempts are counted by the provider. Never retry; an unconfirmed reply
	// may already have consumed an attempt.
	data, err := c.rpcWith(ctx, ramzService, "CheckPassword", &wire.RamzCheckPasswordRequest{Password: p.PIN, ServicesType: ramzServicesTypeBot}, rpcOptions{status: ramzCheckStatus})
	if err != nil {
		return nil, unlockIncomplete(err)
	}
	check := &wire.RamzCheckPasswordResponse{}
	if decode(data, check) != nil || check.Token == "" || len(check.Token) > maxRamzTokenBytes {
		return nil, protocolError()
	}
	request := &wire.FetchProtectedMessageRequest{Peer: peer, MessageId: &wire.MessagePosition{Rid: rid, Date: date}}
	data, err = c.rpcWith(ctx, "bale.messaging.v2.Messaging", protectedMessageMethod, request, rpcOptions{metadata: [][2]string{{ramzTokenMetadataKey, check.Token}}, status: protectedFetchStatus})
	if err != nil {
		return nil, unlockIncomplete(err)
	}
	reply := &wire.FetchProtectedMessageResponse{}
	if decode(data, reply) != nil {
		return nil, protocolError()
	}
	h := reply.History
	if h == nil {
		return nil, protectedUnavailable()
	}
	if validateHistoryItem(h) != nil {
		return nil, protocolError()
	}
	// The unlock must belong to the requested private chat and message.
	if h.Rid != rid || h.Date != date || h.SenderId != peer.Id {
		return nil, protocolError()
	}
	wrapper := h.Message.GetProtected()
	if wrapper == nil {
		return nil, protectedUnsupported()
	}
	inner := wrapper.Message
	if inner == nil {
		return nil, protectedUnavailable()
	}
	content := contentBody(inner, 0)
	if inner.Protected != nil || content["kind"] == "unsupported" {
		return nil, protectedUnsupported()
	}
	return json.Marshal(map[string]any{
		"peer": c.canonicalPeer(p.Peer), "message_id": sid(rid), "date": sid(date),
		"sender_id": uid(h.SenderId), "locked": false, "content": content,
	})
}

func isASCIIDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ramzCheckStatus follows the official client's CheckPassword handling.
// Provider free-form text is intentionally discarded.
func ramzCheckStatus(code int32) error {
	switch code {
	case 16:
		return boundedError("PROTECTED_PIN_REJECTED", "provider rejected the PIN; the attempt was not retried", 403)
	case 10:
		return boundedError("PROTECTED_PIN_ATTEMPTS_LIMITED", "provider limited PIN attempts; wait before trying again", 429)
	case 7:
		return boundedError("PROTECTED_PIN_VERIFICATION_REQUIRED", "provider requires Ramz verification in an official client", 403)
	default:
		return providerError(code, "")
	}
}

func protectedFetchStatus(code int32) error {
	switch code {
	case 16:
		return boundedError("PROTECTED_UNLOCK_REJECTED", "provider rejected the unlock authorization", 403)
	case 5:
		return protectedUnavailable()
	default:
		return providerError(code, "")
	}
}

func protectedUnavailable() error {
	return boundedError("PROTECTED_MESSAGE_UNAVAILABLE", "protected message is unavailable, expired or deleted", 404)
}

func protectedUnsupported() error {
	return boundedError("PROTECTED_MESSAGE_UNSUPPORTED", "protected message content is not a reviewed variant", 422)
}

// unlockIncomplete keeps a timed-out PIN check non-retryable instead of
// reporting it as an unknown send.
func unlockIncomplete(err error) error {
	var de *domains.Error
	if errors.As(err, &de) && de.Ambiguous {
		return &domains.Error{Code: "PROTECTED_UNLOCK_INCOMPLETE", Message: "provider did not confirm the unlock request; it was not retried", HTTP: 504}
	}
	return err
}
