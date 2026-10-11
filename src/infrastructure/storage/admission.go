package storage

import "github.com/mimalef70/goomni/src/domains"

func normalizedLimits(v domains.AdmissionLimits) domains.AdmissionLimits {
	if v.Global <= 0 {
		v.Global = 1000
	}
	if v.Connection <= 0 {
		v.Connection = 100
	}
	return v
}
