package storage

import "github.com/mimalef70/goomni/src/domains"

func (s *Store) acceptancePolicy(provider domains.Provider, kind string) *domains.AcceptancePolicy {
	r, err := s.providers.Get(provider)
	if err != nil {
		return nil
	}
	c, ok := r.Contract.(domains.AcceptanceContract)
	if !ok {
		return nil
	}
	p := c.AcceptancePolicy()
	if p.Kind == "" || p.RequestID == nil || p.Verify == nil || (kind != "" && p.Kind != kind) {
		return nil
	}
	return &p
}
func (s *Store) mediaContract(provider domains.Provider) (domains.PrivateMediaContract, error) {
	r, err := s.providers.Get(provider)
	if err != nil {
		return nil, err
	}
	c, ok := r.Contract.(domains.PrivateMediaContract)
	if !ok {
		return nil, domains.E("INVALID_PROVIDER_MEDIA", "provider has no reviewed private media policy", 502)
	}
	return c, nil
}
