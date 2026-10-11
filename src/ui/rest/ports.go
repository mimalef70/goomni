package rest

import (
	"context"

	"github.com/mimalef70/goomni/src/domains"
)

// OperationalStore exposes bounded diagnostics only, not application persistence.
type OperationalStore interface {
	Ping(context.Context) error
	Stats(context.Context) (map[string]int64, error)
	DiskMetrics() (domains.DiskMetrics, error)
	OperationalMetrics() domains.StorageMetrics
}
