package usecase

import (
	"context"
	"io"
	"sync"
)

type cancelledReader struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
	err    error
}

func (r *cancelledReader) Close() error {
	r.once.Do(func() { r.cancel(); r.err = r.ReadCloser.Close() })
	return r.err
}
