// Package policy contains bounded, provider-independent private-reference rules.
package policy

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/mimalef70/goomni/src/domains"
)

type Media struct{}

func ValidOpaqueID(id string) bool {
	if id == "" || len(id) > 1024 || !utf8.ValidString(id) {
		return false
	}
	for _, c := range id {
		if c < 32 || c == 127 {
			return false
		}
	}
	return true
}
func (Media) ValidateMediaKey(peer domains.Peer, id string) error {
	if err := peer.Validate(); err != nil {
		return err
	}
	if !ValidOpaqueID(id) {
		return domains.E("INVALID_MESSAGE_ID", "message id must be a bounded nonempty UTF-8 string", 400)
	}
	return nil
}
func (Media) ValidateMediaReference(m domains.ProviderMedia) error {
	if !ValidOpaqueID(m.FileID) || len(m.Data) == 0 || len(m.Data) > 1<<20 || !json.Valid(m.Data) {
		return domains.E("INVALID_PROVIDER_MEDIA", "invalid private provider media reference", 502)
	}
	return nil
}
func (Media) ValidateMediaRevision(_ domains.Peer, _ string, revision *domains.MediaRevision) error {
	if revision != nil {
		return domains.E("INVALID_MEDIA_REVISION", "attachment ordering evidence is invalid for this provider and peer", 502)
	}
	return nil
}
func (Media) ProjectMediaAvailability(*domains.Event, bool) error { return nil }
