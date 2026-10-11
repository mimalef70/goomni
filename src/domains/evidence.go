package domains

// CapabilityEvidence describes a dated observation, independent of whether a
// provider is enabled or an operation is admitted. A response is not an effect.
type CapabilityEvidence struct {
	Level       string   `json:"level"`
	Date        string   `json:"date,omitempty"`
	Scope       string   `json:"scope"`
	References  []string `json:"references"`
	Limitations []string `json:"limitations"`
}
