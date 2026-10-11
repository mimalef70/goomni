package domains

import "time"

// Latency contains cumulative, fixed-size buckets; it never retains requests.
type Latency struct {
	Count   uint64
	Seconds float64
	Buckets [9]uint64
}

var latencyBounds = [...]float64{.001, .005, .01, .05, .1, .5, 1, 5}

func (h *Latency) Observe(elapsed time.Duration) {
	seconds := elapsed.Seconds()
	h.Count++
	h.Seconds += seconds
	for i, bound := range latencyBounds {
		if seconds <= bound {
			h.Buckets[i]++
		}
	}
	h.Buckets[len(latencyBounds)]++
}

type PoolStats struct {
	MaxOpenConnections, OpenConnections, InUse, Idle    int
	WaitCount                                           int64
	WaitDuration                                        time.Duration
	MaxIdleClosed, MaxIdleTimeClosed, MaxLifetimeClosed int64
}
type StorageMetrics struct {
	Pool                       PoolStats
	Begin, Transaction, Commit Latency
	Operations                 map[string]Latency
	Errors                     map[string]uint64
}

type DiskMetrics struct{ DatabaseBytes, WALBytes, SHMBytes, FreeBytes int64 }
