package usecase

import (
	"github.com/mimalef70/goomni/src/domains"
)

func (s *serviceRuntime) admissionLimits() domains.AdmissionLimits {
	return domains.AdmissionLimits{Global: s.options.QueueLimit, Connection: s.options.ConnectionQueueLimit}
}
