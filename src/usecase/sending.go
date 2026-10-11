package usecase

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/mimalef70/goomni/src/domains"
)

// Mutate places a reviewed provider mutation in the same durable, per-account
// journal as sends. Neither an API caller nor a retry may choose its wire ID.
func (s *SendService) Mutate(ctx context.Context, id, operation string, payload json.RawMessage, key string) (domains.Operation, error) {
	if strings.TrimSpace(key) == "" || len(key) > 256 {
		return domains.Operation{}, domains.E("IDEMPOTENCY_KEY_REQUIRED", "provide an Idempotency-Key of 1 to 256 bytes for provider mutations", 400)
	}
	device, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.Operation{}, err
	}
	normalized, peer, err := s.normalizeMutation(device, operation, payload)
	if err != nil {
		return domains.Operation{}, err
	}
	request := domains.SendRequest{Kind: "operation", Operation: operation, Payload: normalized, Peer: peer}
	// Media-backed mutations use the same account-scoped admission and retention
	// pin as ordinary sends. A queued avatar must survive manual cleanup/restart.
	var media struct {
		MediaID string `json:"media_id"`
	}
	if err := json.Unmarshal(normalized, &media); err == nil {
		request.MediaID = media.MediaID
	}
	op, _, err := s.sendStore.Enqueue(ctx, device.ConnectionID, request, key, s.admissionLimits())
	return op, err
}

func (s *SendService) ValidateSend(ctx context.Context, id string, r domains.SendRequest) error {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return err
	}
	return s.validateSend(d, r)
}

func (s *SendService) ListOperationsFiltered(ctx context.Context, id string, f domains.OperationFilter) ([]domains.Operation, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	if err = s.validateQueryIdentity(d, f.Peer, ""); err != nil {
		return nil, err
	}
	if err = s.validateOperationFilter(d, f.Operation); err != nil {
		return nil, err
	}
	f.Limit, f.Offset = page(f.Limit, f.Offset)
	return s.sendStore.ListOperationsFiltered(ctx, d.ConnectionID, f)
}

func (s *SendService) Call(ctx context.Context, id, operation string, payload json.RawMessage) (json.RawMessage, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	e, err := s.entry(d)
	if err != nil {
		return nil, err
	}
	contract, err := s.provider(d.Provider)
	if err != nil {
		return nil, err
	}
	definition, ok := operationDefinition(contract, operation)
	if !ok {
		return nil, domains.Unsupported(operation)
	}
	if definition.Mode == "mutation" {
		return nil, domains.E("DURABLE_OPERATION_REQUIRED", "use the durable mutation endpoint with an Idempotency-Key", 409)
	}
	payload, _, err = contract.NormalizeOperation(operation, payload)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, domains.E("DEVICE_NOT_FOUND", "device no longer exists", 404)
	}
	result, err := e.client.Call(ctx, operation, payload)
	return result, safeError(err)
}

func (s *SendService) Send(ctx context.Context, id string, request domains.SendRequest, key string) (domains.Operation, error) {
	if request.Operation != "" || len(request.Payload) > 0 || request.Kind == "operation" {
		return domains.Operation{}, domains.E("INVALID_REQUEST", "use the durable mutation endpoint for provider operations", 400)
	}
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.Operation{}, err
	}
	if err = s.validateSend(d, request); err != nil {
		return domains.Operation{}, err
	}
	if request.IsScheduled() {
		return domains.Operation{}, domains.E("USE_SCHEDULE_ENDPOINT", "use the schedule endpoint for delayed sends", 400)
	}
	if request.RequestID != "" {
		return domains.Operation{}, domains.E("INVALID_REQUEST", "request_id is assigned by the gateway", 400)
	}
	if len(key) > 256 {
		return domains.Operation{}, domains.E("INVALID_IDEMPOTENCY_KEY", "idempotency key exceeds 256 bytes", 400)
	}
	if request.Kind == "" {
		request.Kind = "text"
	}
	// Storage resolves idempotency before checking media, so a completed send
	// can still be inspected through its key after an old asset is retired.
	operation, _, err := s.sendStore.Enqueue(ctx, d.ConnectionID, request, key, s.admissionLimits())
	return operation, err
}

func (s *SendService) GetOperation(ctx context.Context, id, operationID string) (domains.Operation, error) {
	d, err := s.ResolveDevice(ctx, id)
	if err != nil {
		return domains.Operation{}, err
	}
	op, err := s.sendStore.GetOperation(ctx, d.ConnectionID, operationID)
	if err != nil {
		return op, err
	}
	op.Stages, err = s.sendStore.OperationStages(ctx, d.ConnectionID, operationID)
	return op, err
}
