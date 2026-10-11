// Package bale describes Bale admission rules independently of network clients.
package bale

import (
	"encoding/json"
	"maps"
	"strconv"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/providers/evidence"
	"github.com/mimalef70/goomni/src/infrastructure/providers/policy"
	"github.com/mimalef70/goomni/src/internal/balemeow/catalog"
)

type Contract struct{ policy.Media }

func (Contract) Descriptor() domains.ProviderDescriptor {
	return evidence.Provider(domains.ProviderDescriptor{ID: domains.ProviderBale, Name: "Bale", Enabled: true, Interactions: domains.ProviderInteractions{Typing: &domains.OperationPreset{Operation: "presence.typing", Parameters: json.RawMessage(`{"typing_type":1}`)}, TypingStop: &domains.OperationPreset{Operation: "presence.stop", Parameters: json.RawMessage(`{"typing_type":1}`)}, Online: &domains.OperationPreset{Operation: "presence.online", Parameters: json.RawMessage(`{"online":true}`)}, Offline: &domains.OperationPreset{Operation: "presence.online", Parameters: json.RawMessage(`{"online":false}`)}, ReadOperation: "message.read", ReadArgument: "date", ReceiptModel: "timestamp_watermark", ReadEvents: true, DeliveredEvents: true, SenderNames: "enriched", AvatarDownload: true}, DeliveryMethods: []string{"provider"}, Send: domains.ProviderSendCapabilities{Kinds: []string{"text", "file", "image", "audio", "voice", "video"}, MaxTextBytes: 65536, MentionsSupported: true, MaxMentions: 100, ReplySupported: true, MediaFormatNotes: domains.ProviderMediaFormatNotes{Voice: "Complete single-stream Ogg Opus, mapping 0 mono/stereo; inspected without transcoding."}}})
}
func (Contract) Operations() []domains.OperationContract {
	list := catalog.OperationDefinitions()
	list = append(list, basicOperations()...)
	for i := range list {
		if list[i].Operation == "account.name" {
			list[i].Request.Properties = maps.Clone(list[i].Request.Properties)
			list[i].Request.Properties["push_name"] = list[i].Request.Properties["name"]
			delete(list[i].Request.Properties, "name")
			list[i].Request.Required = []string{"push_name"}
		}

		switch list[i].Operation {
		case "message.forward", "send.poll", "send.sticker", "send.contact", "send.location", "send.template":
			list[i].Schedulable = true
		}
	}
	return evidence.Operations(domains.ProviderBale, list)
}
func (Contract) ValidatePeer(p domains.Peer) error {
	if err := p.Validate(); err != nil {
		return err
	}
	n, err := strconv.ParseUint(p.ID, 10, 32)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != p.ID {
		return domains.E("INVALID_PEER", "Bale peer.id must be a positive uint32 decimal string", 400)
	}
	return nil
}
func (Contract) ValidateUserID(id string) bool { return domains.CanonicalUserID(id) }
func (c Contract) ValidateSend(r domains.SendRequest) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if r.Phone == "" {
		if err := c.ValidatePeer(r.Peer); err != nil {
			return err
		}
	}
	switch r.Kind {
	case "", "text", "file", "image", "audio", "voice", "video":
	default:
		return domains.Unsupported("send." + r.Kind)
	}
	for _, id := range r.Mentions {
		if !c.ValidateUserID(id) {
			return domains.E("INVALID_REQUEST", "Bale mentions require canonical positive uint32 user IDs", 400)
		}
	}
	if r.ReplyMessageID != "" && !ValidMessageID(r.ReplyMessageID) {
		return domains.E("INVALID_REQUEST", "Bale reply_message_id must be a nonzero signed int64", 400)
	}
	return nil
}
func (Contract) NormalizeOperation(name string, raw json.RawMessage) (json.RawMessage, domains.Peer, error) {
	if name == "account.name" {
		for _, def := range (Contract{}).Operations() {
			if def.Operation == name {
				normalized, peer, err := domains.NormalizeOperationContract(def, raw)
				if err != nil {
					return nil, peer, err
				}
				var fields map[string]json.RawMessage
				if err = json.Unmarshal(normalized, &fields); err != nil {
					return nil, peer, err
				}
				raw, err = json.Marshal(map[string]json.RawMessage{"name": fields["push_name"]})
				if err != nil {
					return nil, peer, err
				}
				break
			}
		}
	}

	if def, ok := catalog.OperationDefinition(name); ok {
		if def.Mode == "mutation" {
			return NormalizeMutation(name, raw)
		}
		return catalog.NormalizeOperation(name, raw)
	}
	for _, def := range (Contract{}).Operations() {
		if def.Operation == name {
			if def.Mode == "mutation" {
				return NormalizeMutation(name, raw)
			}
			if len(raw) == 0 {
				raw = json.RawMessage(`{}`)
			}
			return domains.NormalizeOperationContract(def, raw)
		}
	}
	return nil, domains.Peer{}, domains.Unsupported(name)
}
func (Contract) ValidateSession(s *domains.Session) error {
	if s == nil || s.Provider != domains.ProviderBale || s.Version != 1 || !domains.CanonicalUserID(s.UserID) || s.Token == "" {
		return domains.E("INVALID_PROVIDER_SESSION", "provider returned an incomplete Bale session", 502)
	}
	return nil
}
