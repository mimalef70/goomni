package providers

import (
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/providers/bale"
	"github.com/mimalef70/goomni/src/infrastructure/providers/eitaa"
	"github.com/mimalef70/goomni/src/infrastructure/providers/rubika"
)

// Contracts includes disabled providers' policies so existing encrypted history
// remains readable. It starts no clients and requires no live credentials.
func Contracts() *domains.ProviderRegistry {
	r, err := domains.NewProviderRegistry(domains.ProviderRegistration{Contract: bale.Contract{}}, domains.ProviderRegistration{Contract: eitaa.Contract{}}, domains.ProviderRegistration{Contract: rubika.Contract{}})
	if err != nil {
		panic("invalid built-in provider contracts")
	}
	return r
}
