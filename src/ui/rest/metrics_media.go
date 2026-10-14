package rest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/mimalef70/goomni/src/pkg/platform"
)

// An incremental depth-first scan keeps at most 16 directories open and handles
// at most 2048 entries per sampler tick. Symlinks are never followed. Scrapes
// only see the last completed pass, whose timestamp exposes eventual freshness.
type mediaScanner struct {
	stack []*os.File
	bytes int64
}

func (s *mediaScanner) close() {
	for _, f := range s.stack {
		_ = f.Close()
	}
	s.stack = nil
	s.bytes = 0
}

func (s *mediaScanner) step(ctx context.Context, root string) (total int64, complete bool, err error) {
	defer func() {
		if err != nil {
			s.close()
		}
	}()
	if len(s.stack) == 0 {
		if root == "" {
			return 0, true, nil
		}
		dir, e := platform.OpenDirNoFollow(root)
		if os.IsNotExist(e) {
			return 0, true, nil
		}
		if e != nil {
			return 0, false, e
		}
		s.stack = []*os.File{dir}
		s.bytes = 0
	}
	for n := 0; n < 2048; n++ {
		if ctx.Err() != nil {
			return 0, false, nil
		}
		f := s.stack[len(s.stack)-1]
		entries, e := f.ReadDir(1)
		if e != nil && e != io.EOF {
			return 0, false, e
		}
		if len(entries) == 0 {
			_ = f.Close()
			s.stack = s.stack[:len(s.stack)-1]
			if len(s.stack) == 0 {
				total = s.bytes
				s.bytes = 0
				return total, true, nil
			}
			continue
		}
		entry := entries[0]
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if entry.IsDir() {
			if len(s.stack) == 16 {
				return 0, false, fmt.Errorf("media directory nesting exceeds metric scan limit")
			}
			child, e := platform.OpenDirAt(f, entry.Name())
			if errors.Is(e, platform.ErrSkip) {
				continue
			}
			if e != nil {
				return 0, false, e
			}
			s.stack = append(s.stack, child)
			continue
		}
		info, e := entry.Info()
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return 0, false, e
		}
		if info.Mode().IsRegular() {
			s.bytes += info.Size()
		}
	}
	return 0, false, nil
}
