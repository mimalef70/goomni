package usecase

import (
	"encoding/json"

	"github.com/mimalef70/goomni/src/domains"
)

func mutationWirePayload(request domains.SendRequest) (json.RawMessage, error) {
	if request.Kind != "operation" || request.Operation == "" || !domains.ValidOpaqueID(request.RequestID) {
		return nil, domains.E("INVALID_OPERATION", "invalid persisted provider mutation", 500)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(request.Payload, &fields); err != nil {
		return nil, err
	}
	fields["request_id"], _ = json.Marshal(request.RequestID)
	return json.Marshal(fields)
}
