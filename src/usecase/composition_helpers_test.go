package usecase

import (
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/providers/bale"
)

func newTestService(store Store, options Options, factory domains.ClientFactory) *Service {
	if options.Providers == nil {
		options.Providers, _ = domains.NewProviderRegistry(domains.ProviderRegistration{Contract: bale.Contract{}, Factory: factory})
	}
	return New(store, options)
}
