package bale

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPublicNameContractNormalizesBeforeJournal(t *testing.T) {
	c := Contract{}
	for _, input := range []string{`{"name":"synthetic"}`, `{"push_name":"synthetic","name":"ambiguous"}`, `{"push_name":null}`} {
		_, _, err := c.NormalizeOperation("account.name", json.RawMessage(input))
		require.Error(t, err)
	}
	output, _, err := c.NormalizeOperation("account.name", json.RawMessage(`{"push_name":"نام 🙂"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"نام 🙂"}`, string(output))
	for _, operation := range c.Operations() {
		if operation.Operation != "account.name" {
			continue
		}
		require.Contains(t, operation.Request.Properties, "push_name")
		require.NotContains(t, operation.Request.Properties, "name")
	}
}
