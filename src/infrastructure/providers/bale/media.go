package bale

import (
	"encoding/json"
	"strconv"

	"github.com/mimalef70/goomni/src/domains"
)

func (Contract) ValidateMediaKey(peer domains.Peer, id string) error {
	if err := peer.Validate(); err != nil {
		return err
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n == 0 || len(id) == 0 || id[0] == '+' {
		return domains.E("INVALID_MESSAGE_ID", "a nonzero signed decimal message id is required", 400)
	}
	return nil
}
func (Contract) ValidateMediaReference(m domains.ProviderMedia) error {
	n, err := strconv.ParseInt(m.FileID, 10, 64)
	if err != nil || n == 0 {
		return domains.E("INVALID_PROVIDER_MEDIA", "invalid provider file id", 502)
	}
	if _, err = strconv.ParseInt(m.AccessHash, 10, 64); err != nil {
		return domains.E("INVALID_PROVIDER_MEDIA", "invalid provider media access reference", 502)
	}
	return nil
}
func (Contract) ProjectMediaAvailability(event *domains.Event, available bool) error {
	id := ""
	if event.Media != nil {
		id = event.Media.FileID
	}
	payload, err := setContentMediaAvailability(event.Payload, id, available, 0)
	if err != nil {
		return err
	}
	event.Payload = payload
	return nil
}
func setContentMediaAvailability(raw json.RawMessage, fileID string, available bool, depth int) (json.RawMessage, error) {
	if len(raw) == 0 || depth >= 8 {
		return raw, nil
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	var kind string
	_ = json.Unmarshal(body["kind"], &kind)
	changed := false
	if kind == "document" {
		var advertised string
		_ = json.Unmarshal(body["file_id"], &advertised)
		body["download_supported"] = json.RawMessage("false")
		if available && fileID != "" && advertised == fileID {
			body["download_supported"] = json.RawMessage("true")
		}
		changed = true
	}
	if kind == "template" || kind == "template_response" {
		if content, ok := body["content"]; ok {
			updated, err := setContentMediaAvailability(content, fileID, available, depth+1)
			if err != nil {
				return nil, err
			}
			body["content"] = updated
			changed = true
		}
	}
	if !changed {
		return raw, nil
	}
	return json.Marshal(body)
}
