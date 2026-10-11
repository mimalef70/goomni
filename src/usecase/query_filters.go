package usecase

import (
	"github.com/mimalef70/goomni/src/domains"
)

func (s *serviceRuntime) validateOperationFilter(d domains.Device, operation string) error {
	if operation == "" {
		return nil
	}
	r, err := s.options.Providers.Get(d.Provider)
	if err != nil {
		return err
	}
	def, ok := operationDefinition(r.Contract, operation)
	if !ok || def.Mode != "mutation" {
		return domains.E("INVALID_FILTER", "operation is not a mutation for this provider", 400)
	}
	return nil
}
