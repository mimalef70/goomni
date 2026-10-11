package evidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/stretchr/testify/require"
)

func TestEvidenceReferencesAndObservationLevels(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
	check := func(record domains.CapabilityEvidence) {
		t.Helper()
		require.Contains(t, []string{"offline_only", "live_response", "live_scoped", "live_failed"}, record.Level)
		require.NotEmpty(t, record.Scope)
		require.NotEmpty(t, record.References)
		if record.Date != "" {
			_, err := time.Parse(time.DateOnly, record.Date)
			require.NoError(t, err)
		}
		if record.Level != "offline_only" {
			require.NotEmpty(t, record.Date)
		}
		for _, reference := range record.References {
			path, pointer, _ := strings.Cut(reference, "#")
			require.False(t, filepath.IsAbs(path))
			require.NotContains(t, path, "..")
			data, err := os.ReadFile(filepath.Join(root, path))
			require.NoError(t, err)
			if pointer == "" {
				continue
			}
			var value any
			require.NoError(t, json.Unmarshal(data, &value))
			for _, part := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
				switch current := value.(type) {
				case map[string]any:
					var exists bool
					value, exists = current[part]
					require.True(t, exists, reference)
				case []any:
					i, err := strconv.Atoi(part)
					require.NoError(t, err)
					require.GreaterOrEqual(t, i, 0)
					require.Less(t, i, len(current))
					value = current[i]
				default:
					t.Fatalf("invalid evidence pointer %s", reference)
				}
			}
		}
	}
	for provider, record := range records {
		t.Run(string(provider), func(t *testing.T) {
			check(record.Provider)
			for _, observation := range record.Send {
				check(observation)
			}
			for _, observation := range record.Operations {
				check(observation)
			}
		})
	}
	// Dated failure and response observations must never become full semantic
	// verification just because a method exists or returned HTTP 200.
	require.Equal(t, "live_failed", records[domains.ProviderEitaa].Operations["send.poll"].Level)
	require.Equal(t, "live_scoped", records[domains.ProviderEitaa].Operations["send.location"].Level)
	require.Equal(t, "live_scoped", records[domains.ProviderRubika].Operations["send.contact"].Level)
	require.Equal(t, "offline_only", records[domains.ProviderBale].Operations["account.about"].Level)
	for _, provider := range records {
		for _, observation := range provider.Operations {
			if observation.Level == "live_response" {
				require.Contains(t, summary(observation), "semantic acceptance pending")
				require.NotEmpty(t, observation.Limitations)
			}
		}
	}
}

func TestEvidenceDiscoveryReturnsIndependentRecords(t *testing.T) {
	d := domains.ProviderDescriptor{ID: domains.ProviderEitaa, Send: domains.ProviderSendCapabilities{Kinds: []string{"text"}}}
	first := Provider(d)
	first.Evidence.References[0] = "changed"
	first.Send.Evidence["text"] = domains.CapabilityEvidence{Level: "changed"}
	second := Provider(d)
	require.NotEqual(t, "changed", second.Evidence.References[0])
	require.Equal(t, "live_scoped", second.Send.Evidence["text"].Level)
	list := []domains.OperationContract{{Operation: "not-in-ledger"}}
	list = Operations(domains.ProviderEitaa, list)
	require.Equal(t, "offline_only", list[0].Evidence.Level)
	require.Contains(t, list[0].Verification, "pending")
}
