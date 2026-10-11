package providers_test

import (
	"encoding/json"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/providers/bale"
	"github.com/mimalef70/goomni/src/infrastructure/providers/eitaa"
	"github.com/mimalef70/goomni/src/infrastructure/providers/rubika"
	"github.com/stretchr/testify/require"
)

func TestInteractionPresetsPassTheAdvertisedPublicContract(t *testing.T) {
	for _, tc := range []struct {
		c    domains.ProviderContract
		peer domains.Peer
	}{
		{bale.Contract{}, domains.Peer{Type: "user", ID: "7"}},
		{eitaa.Contract{}, domains.Peer{Type: "user", ID: "7"}},
		{rubika.Contract{}, domains.Peer{Type: "user", ID: "u0synthetic"}},
	} {
		t.Run(string(tc.c.Descriptor().ID), func(t *testing.T) {
			interactions := tc.c.Descriptor().Interactions
			for _, preset := range []*domains.OperationPreset{interactions.Typing, interactions.TypingStop, interactions.Online, interactions.Offline} {
				if preset == nil {
					continue
				}
				var body map[string]any
				require.NoError(t, json.Unmarshal(preset.Parameters, &body))
				require.NotContains(t, body, "request_id")
				require.NotContains(t, body, "peer")
				var definition domains.OperationContract
				for _, op := range tc.c.Operations() {
					if op.Operation == preset.Operation {
						definition = op
					}
				}
				require.NotEmpty(t, definition.Operation)
				if _, ok := definition.Request.Properties["peer"]; ok {
					body["peer"] = tc.peer
				}
				raw, err := json.Marshal(body)
				require.NoError(t, err)
				_, _, err = tc.c.NormalizeOperation(preset.Operation, raw)
				require.NoError(t, err)
			}
			body := map[string]any{"peer": tc.peer, interactions.ReadArgument: "123"}
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			_, _, err = tc.c.NormalizeOperation(interactions.ReadOperation, raw)
			require.NoError(t, err)
		})
	}
	// No stop/online or receipt event is invented for Rubika's chat state.
	r := (rubika.Contract{}).Descriptor().Interactions
	require.Nil(t, r.TypingStop)
	require.Nil(t, r.Online)
	require.Nil(t, r.Offline)
	require.False(t, r.ReadEvents)
	require.False(t, r.DeliveredEvents)
	require.True(t, r.PartialEdits)
	e := (eitaa.Contract{}).Descriptor().Interactions
	require.Nil(t, e.Typing)
	require.Nil(t, e.TypingStop)
	require.Nil(t, e.Online)
	require.False(t, e.DeliveredEvents)
	require.Equal(t, "message_id_watermark", e.ReceiptModel)
}
