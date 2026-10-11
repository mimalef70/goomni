package domains

import "fmt"

// WebhookTarget is a routing snapshot. Device targets fetch the latest secret
// when claimed; global targets retain an encrypted secret snapshot.
type WebhookTarget struct {
	URL      string
	Secret   string
	Revision int64
	Device   bool
	Events   []string
	// CurrentDevice is a routing plan resolved under the event transaction.
	CurrentDevice bool
	MergeGlobal   bool
}
type Chat struct {
	Peer      Peer  `json:"peer"`
	LastEvent Event `json:"last_event"`
	Count     int   `json:"count"`
}

// AdmissionLimits bound outstanding work, including unresolved outcomes.
type AdmissionLimits struct{ Global, Connection int }

// String never includes destination credentials.
func (t WebhookTarget) String() string {
	return fmt.Sprintf("WebhookTarget{device:%t,revision:%d}", t.Device, t.Revision)
}
