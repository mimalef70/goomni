package eitaa

import (
	"encoding/json"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/providers/evidence"
	"github.com/mimalef70/goomni/src/infrastructure/providers/policy"
	"github.com/mimalef70/goomni/src/internal/eitaameow"
)

type Contract struct {
	policy.Media
	Enabled bool
}

func (c Contract) Descriptor() domains.ProviderDescriptor {
	return evidence.Provider(domains.ProviderDescriptor{ID: domains.ProviderEitaa, Name: "Eitaa", Enabled: c.Enabled, Interactions: domains.ProviderInteractions{ReadOperation: "message.read", ReadArgument: "message_id", ReceiptModel: "message_id_watermark", ReadEvents: true, SenderNames: "enriched", AvatarDownload: true}, DeliveryMethods: []string{"sms", "call", "flash_call", "app"}, Send: domains.ProviderSendCapabilities{Kinds: []string{"text", "file", "image", "audio", "voice", "video"}, MaxTextCharacters: 4096, MentionsSupported: false, MaxMentions: 0, ReplySupported: true, MediaFormatNotes: domains.ProviderMediaFormatNotes{Voice: "Complete single-stream Ogg Opus, mapping 0 mono/stereo; inspected without transcoding.", Audio: "Complete single-stream Ogg Opus; send other codecs as files.", Video: "Nonfragmented AVC MP4 with inspected dimensions/timing and an optional real first-frame JPEG preview; no transcoding or playback guarantee. The provider can return short clips as files; inspect received media.type."}}})
}
func (Contract) ValidateUserID(s string) bool             { return eitaameow.ValidateUserID(s) }
func (Contract) ValidatePeer(p domains.Peer) error        { return eitaameow.ValidatePeer(p) }
func (Contract) ValidateSend(r domains.SendRequest) error { return eitaameow.ValidateSend(r) }
func (Contract) ValidateSession(s *domains.Session) error { return eitaameow.ValidateSession(s) }
func (Contract) Operations() []domains.OperationContract {
	list := eitaameow.Operations()
	for i := range list {
		list[i].Method = "POST"
		list[i].Path = "/operations/" + list[i].Operation
	}
	return evidence.Operations(domains.ProviderEitaa, list)
}
func (Contract) NormalizeOperation(name string, raw json.RawMessage) (json.RawMessage, domains.Peer, error) {
	return eitaameow.NormalizeOperation(name, raw)
}
