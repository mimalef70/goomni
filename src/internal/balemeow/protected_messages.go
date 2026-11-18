package balemeow

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

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
	// Resolve the current message before spending a PIN attempt.
	date, err := c.resolveProtectedDate(ctx, p.Peer, sid(rid))
	if err != nil {
		return nil, err
	}
	peer, err := c.protectedPeer(ctx, p.Peer)
	if err != nil {
		return nil, err
	}

	// Attempts are counted by the provider. Never retry; an unconfirmed reply
	// may already have consumed an attempt.
	data, err := c.rpcWith(ctx, ramzService, "CheckPassword", &wire.RamzCheckPasswordRequest{Password: p.PIN, ServicesType: ramzServicesTypeBot}, rpcOptions{status: ramzCheckStatus, keepSession: true})
	if err != nil {
		return nil, unlockIncomplete(err)
	}
	check := &wire.RamzCheckPasswordResponse{}
	if decode(data, check) != nil || check.Token == "" || len(check.Token) > maxRamzTokenBytes {
		return nil, protocolError()
	}
	sequence := int64(0)
	request := &wire.FetchProtectedMessageRequest{Peer: peer, MessageId: &wire.ProtectedFetchPosition{Rid: rid, Date: date, Sequence: &sequence}}
	// A rejected fetch concerns the per-request Ramz token, not the account
	// session, so keep the session on its UNAUTHENTICATED reply.
	data, err = c.rpcWith(ctx, "bale.messaging.v2.Messaging", protectedMessageMethod, request, rpcOptions{metadata: [][2]string{{ramzTokenMetadataKey, check.Token}}, status: protectedFetchStatus, keepSession: true})
	if err != nil {
		c.reportDiagnostic("PROTECTED_PIN_ACCEPTED_FETCH_FAILED")
		return nil, unlockIncomplete(err)
	}
	reply := &wire.FetchProtectedMessageResponse{}
	if decode(data, reply) != nil {
		return nil, boundedError("PROTECTED_FETCH_INVALID", "invalid protected message response", 502)
	}
	// From here the PIN was accepted and a token obtained; any failure is in the
	// fetch phase. The codes below distinguish the exact cause for diagnosis.
	h := reply.History
	if h == nil {
		return nil, boundedError("PROTECTED_FETCH_EMPTY", "the PIN was accepted but the provider returned no message for the unlock", 502)
	}
	if validateHistoryItem(h) != nil {
		return nil, boundedError("PROTECTED_FETCH_INVALID", "invalid protected history item", 502)
	}
	// The unlock must belong to the requested private chat and message.
	if h.Rid != rid || h.Date != date || h.SenderId != uint32(peer.Id) {
		return nil, boundedError("PROTECTED_IDENTITY_MISMATCH", "the provider returned a different message than requested", 502)
	}
	wrapper := h.Message.GetProtected()
	if wrapper == nil {
		return nil, protectedUnsupported()
	}
	inner := wrapper.Message
	if inner == nil {
		return nil, boundedError("PROTECTED_CONTENT_EMPTY", "the provider returned a protected wrapper with no content", 422)
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
		return boundedError("PROTECTED_PIN_VERIFICATION_REQUIRED", "provider requires SMS verification; run message.protected.verify.start then message.protected.verify.submit", 403)
	default:
		return providerError(code, "")
	}
}

func protectedFetchStatus(code int32) error {
	switch code {
	case 16:
		return boundedError("PROTECTED_UNLOCK_REJECTED", "provider rejected the unlock authorization", 403)
	case 5:
		// The PIN was accepted; the provider could not match this id and date.
		return boundedError("PROTECTED_MESSAGE_NOT_FOUND", "the PIN was accepted but the provider did not find a protected message with this id and date for the peer", 404)
	default:
		return providerError(code, "")
	}
}

// ProtectedMessage.tsx constructs an extended peer with a zero access hash.
func (c *Client) protectedPeer(ctx context.Context, public domains.Peer) (*wire.ProtectedFetchPeer, error) {
	ref, err := c.accountUserRef(ctx, public)
	if err != nil {
		return nil, err
	}
	data, err := c.readRPC(ctx, accountUsersService, "LoadUsers", &wire.AccountPeersRequest{Peers: []*wire.PeerRef{ref}})
	if err != nil {
		return nil, err
	}
	var reply wire.AccountUsersResponse
	if decode(data, &reply) != nil || len(reply.Users) != 1 || reply.Users[0].GetId() != ref.Id {
		return nil, protocolError()
	}
	fetchPeer := &wire.ProtectedFetchPeer{Type: 1, Id: int32(ref.Id)}
	if reply.Users[0].GetIsBot().GetValue() {
		fetchPeer.Type = 4
	}
	return fetchPeer, nil
}

func (c *Client) resolveProtectedDate(ctx context.Context, peer domains.Peer, messageID string) (int64, error) {
	if c.opts.ResolveMessageDate != nil {
		ms, found, err := c.opts.ResolveMessageDate(ctx, peer, messageID)
		if err != nil {
			return 0, boundedError("PROTECTED_METADATA_LOOKUP_FAILED", "could not resolve the protected message date", 500)
		}
		if found && ms > 0 {
			return ms, nil
		}
	}

	// LoadHistory takes a date cursor, with no RID-only filter. Bound the
	// fallback to one page. The cursor is never used as the unlock date.
	native, err := c.messagePeer(ctx, peer)
	if err != nil {
		return 0, err
	}
	const limit = 100
	data, err := c.readRPC(ctx, "bale.messaging.v2.Messaging", "LoadHistory", &wire.HistoryRequest{Peer: native, Date: time.Now().UnixMilli(), LoadMode: 2, Limit: limit})
	if err != nil {
		return 0, err
	}
	var page wire.HistoryResponse
	if decode(data, &page) != nil || len(page.History) > limit {
		return 0, protocolError()
	}
	for _, item := range page.History {
		if item.GetSenderId() == native.Id && sid(item.GetRid()) == messageID && item.GetMessage().GetProtected() != nil {
			if validateHistoryItem(item) != nil {
				return 0, protocolError()
			}
			return item.Date, nil
		}
	}
	return 0, boundedError("PROTECTED_MESSAGE_METADATA_NOT_FOUND", "protected message metadata unavailable in stored events and recent chat history", 404)
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

// startProtectedVerification requests the provider SMS code used to authorize the
// account session for protected-message reads (Ramz SendOTP). It is a mutation:
// it is never auto-retried, and it sends no account data in the request.
func (c *Client) startProtectedVerification(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if err := requireEmptyBody(raw); err != nil {
		return nil, err
	}
	// SendOTP takes no arguments; the session identifies the account.
	_, err := c.rpcWith(ctx, ramzService, "SendOTP", &wire.Empty{}, rpcOptions{status: ramzSendOTPStatus})
	if err != nil {
		return nil, verificationIncomplete(err, "the verification code may have been sent; do not resend immediately")
	}
	return json.RawMessage(`{"verification_sent":true}`), nil
}

// submitProtectedVerification submits the SMS code the account owner received
// (Ramz ValidateOTP). The code is used only for this one request: it is never
// stored, logged, retried or reused. On success the account session is
// authorized and message.protected.unlock can be retried with the PIN.
func (c *Client) submitProtectedVerification(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Code string `json:"verification_code"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return nil, boundedError("INVALID_REQUEST", "invalid verification body", 400)
	}
	code, err := ramzOTP(p.Code)
	if err != nil {
		return nil, err
	}
	// ValidateOTP mutates the session authorization; never auto-retry it.
	data, err := c.rpcWith(ctx, ramzService, "ValidateOTP", &wire.RamzValidateOTPRequest{Otp: code, ServicesType: ramzServicesTypeBot}, rpcOptions{status: ramzValidateStatus})
	if err != nil {
		return nil, verificationIncomplete(err, "the verification result is unconfirmed; it was not retried")
	}
	reply := &wire.RamzValidateOTPResponse{}
	if decode(data, reply) != nil {
		return nil, protocolError()
	}
	if !reply.OtpValid {
		return nil, boundedError("PROTECTED_VERIFICATION_REJECTED", "provider rejected the verification code; it was not retried", 403)
	}
	return json.RawMessage(`{"verified":true}`), nil
}

func requireEmptyBody(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 0 {
		return boundedError("INVALID_REQUEST", "this operation takes no fields", 400)
	}
	return nil
}

// ramzOTP parses the SMS code exactly as the official client does (Number(code)
// into an int32). Leading zeros are not significant on the wire.
func ramzOTP(s string) (int32, error) {
	if l := len(s); l < 4 || l > 9 || !isASCIIDigits(s) {
		return 0, boundedError("INVALID_REQUEST", "verification_code must be 4 to 9 ASCII digits", 400)
	}
	n, err := strconv.ParseInt(s, 10, 32)
	if err != nil || n <= 0 {
		return 0, boundedError("INVALID_REQUEST", "verification_code is out of range", 400)
	}
	return int32(n), nil
}

// ramzSendOTPStatus maps SendOTP provider codes. ABORTED (10) and ALREADY_EXISTS
// (6) mean a code was already sent too recently.
func ramzSendOTPStatus(code int32) error {
	switch code {
	case 6, 10:
		return boundedError("PROTECTED_VERIFICATION_RATE_LIMITED", "a verification code was already sent; wait before requesting another", 429)
	default:
		return providerError(code, "")
	}
}

// ramzValidateStatus maps ValidateOTP provider codes. ABORTED (10) is the
// provider attempt limit. A wrong code is a successful response with
// otp_valid=false, handled by the caller, not an error here.
func ramzValidateStatus(code int32) error {
	switch code {
	case 10:
		return boundedError("PROTECTED_VERIFICATION_RATE_LIMITED", "provider limited verification attempts; wait before trying again", 429)
	default:
		return providerError(code, "")
	}
}

// verificationIncomplete keeps a timed-out SMS step non-retryable instead of
// reporting it as an unknown send.
func verificationIncomplete(err error, detail string) error {
	var de *domains.Error
	if errors.As(err, &de) && de.Ambiguous {
		return &domains.Error{Code: "PROTECTED_VERIFICATION_INCOMPLETE", Message: detail, HTTP: 504}
	}
	return err
}
