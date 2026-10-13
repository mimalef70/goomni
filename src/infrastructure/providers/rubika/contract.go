package rubika

import (
	"encoding/json"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/providers/evidence"
	"github.com/mimalef70/goomni/src/infrastructure/providers/policy"
	"github.com/mimalef70/goomni/src/internal/rubikameow"
)

type Contract struct {
	policy.Media
	Enabled bool
}

func (c Contract) Descriptor() domains.ProviderDescriptor {
	return evidence.Provider(domains.ProviderDescriptor{ID: domains.ProviderRubika, Name: "Rubika", Enabled: c.Enabled, Interactions: domains.ProviderInteractions{Typing: &domains.OperationPreset{Operation: "chat.activity", Parameters: json.RawMessage(`{"activity":"Typing"}`)}, ReadOperation: "message.read", ReadArgument: "message_id", ReceiptModel: "chat_state", SenderNames: "enriched", AvatarDownload: true, PartialEdits: true}, DeliveryMethods: []string{"sms", "app", "call"}, Send: domains.ProviderSendCapabilities{Kinds: []string{"text", "file", "image", "audio", "voice", "video"}, MaxTextCharacters: 4200, MentionsSupported: false, MaxMentions: 0, ReplySupported: true, MediaFormatNotes: domains.ProviderMediaFormatNotes{Voice: "Complete single-stream Ogg Opus, mapping 0 mono/stereo; inspected without transcoding.", Audio: "Complete MPEG layer III (MP3), at least one second, with duration inspected from frames; Ogg Opus uses voice.", Video: "Nonfragmented MP4 with one AVC video track and a decodable first IDR frame; real thumbnail, dimensions and millisecond timing are derived without transcoding."}}})
}
func (Contract) ValidateUserID(id string) bool            { return rubikameow.ValidateUserID(id) }
func (Contract) SupportsPeerType(kind string) bool        { return rubikameow.SupportsPeerType(kind) }
func (Contract) ValidateSenderID(id string) bool          { return rubikameow.ValidateSenderID(id) }
func (Contract) ValidatePeer(p domains.Peer) error        { return rubikameow.ValidatePeer(p) }
func (Contract) ValidateSend(r domains.SendRequest) error { return rubikameow.ValidateSend(r) }
func (Contract) ValidateSession(s *domains.Session) error { return rubikameow.ValidateSession(s) }
func (Contract) Operations() []domains.OperationContract {
	list := rubikameow.Operations()
	for i := range list {
		list[i].Method = "POST"
		list[i].Path = "/operations/" + list[i].Operation
	}
	return evidence.Operations(domains.ProviderRubika, list)
}
func (Contract) NormalizeOperation(name string, raw json.RawMessage) (json.RawMessage, domains.Peer, error) {
	return rubikameow.NormalizeOperation(name, raw)
}
