package rest

import (
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/providers/bale"
	"github.com/mimalef70/goomni/src/infrastructure/storage"
	"github.com/mimalef70/goomni/src/usecase"
)

func newTestService(store usecase.Store, options usecase.Options, factory domains.ClientFactory) *usecase.Service {
	if options.Providers == nil {
		options.Providers, _ = domains.NewProviderRegistry(domains.ProviderRegistration{Contract: bale.Contract{}, Factory: factory})
	}
	return usecase.New(store, options)
}

func (s *Server) testStore() *storage.Store { return s.store.(*storage.Store) }
