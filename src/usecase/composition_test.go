package usecase

// Only synthetic fixture reconstruction needs the complete composition port.
func (s *Service) storePorts() Store { return s.serviceRuntime.accountStore.(Store) }
