// Package evidence projects the dated acceptance records into discovery and
// generated documentation. It never enables a provider or contacts a network.
package evidence

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/mimalef70/goomni/src/domains"
)

//go:embed records.json
var encoded []byte

type providerRecord struct {
	Provider   domains.CapabilityEvidence            `json:"provider"`
	Send       map[string]domains.CapabilityEvidence `json:"send"`
	Operations map[string]domains.CapabilityEvidence `json:"operations"`
}

var records = func() map[domains.Provider]providerRecord {
	var result map[domains.Provider]providerRecord
	if json.Unmarshal(encoded, &result) != nil {
		panic("invalid provider evidence records")
	}
	return result
}()

func copyRecord(r domains.CapabilityEvidence) domains.CapabilityEvidence {
	r.References = append([]string{}, r.References...)
	r.Limitations = append([]string{}, r.Limitations...)
	return r
}

func Pending(scope string) domains.CapabilityEvidence {
	return domains.CapabilityEvidence{Level: "offline_only", Scope: scope, References: []string{"docs/providers/acceptance.md"}, Limitations: []string{"Live semantic acceptance remains pending."}}
}

func summary(r domains.CapabilityEvidence) string {
	switch r.Level {
	case "live_scoped":
		return fmt.Sprintf("Scoped live evidence (%s); see scope and limitations", r.Date)
	case "live_response":
		return fmt.Sprintf("Live response observed (%s); semantic acceptance pending", r.Date)
	case "live_failed":
		return fmt.Sprintf("Unsuccessful live attempt (%s); see recorded limitations", r.Date)
	default:
		return "Synthetic checks; live semantic acceptance pending"
	}
}

func Provider(d domains.ProviderDescriptor) domains.ProviderDescriptor {
	r, ok := records[d.ID]
	if !ok {
		d.Evidence = Pending("Provider is not registered.")
	} else {
		d.Evidence = copyRecord(r.Provider)
	}
	d.Verification = summary(d.Evidence)
	d.Send.Evidence = map[string]domains.CapabilityEvidence{}
	for _, kind := range d.Send.Kinds {
		e, ok := r.Send[kind]
		if !ok {
			e = Pending("Registered send contract with synthetic checks.")
		}
		d.Send.Evidence[kind] = copyRecord(e)
	}
	return d
}

func Operations(provider domains.Provider, list []domains.OperationContract) []domains.OperationContract {
	for i := range list {
		r, ok := records[provider].Operations[list[i].Operation]
		if !ok {
			r = Pending("Registered operation contract with synthetic checks.")
		}
		list[i].Evidence = copyRecord(r)
		list[i].Verification = summary(r)
	}
	return list
}
