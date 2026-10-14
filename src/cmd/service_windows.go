//go:build windows

package cmd

import (
	"context"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/sys/windows/svc"
)

// stopTimeout bounds how long a service stop waits for the graceful shutdown
// (HTTP drain, workers, SQLite close) before reporting stopped anyway.
const stopTimeout = 60 * time.Second

// runAsService runs GoOmni under the Windows Service Control Manager when the
// SCM started the process (see docs/windows.md for installing the service).
// It reports false when started from a console, which then runs as usual.
//
// A service starts in C:\Windows\System32, so the working directory becomes
// the executable's folder: .env, master.key and the relative storages/ paths
// resolve there as they do when `goomni rest` runs from that folder. A service
// has no console: the log goes to logs\goomni-service.log (size-capped, see
// rotatingFile), and a panic or fatal runtime error goes to
// logs\goomni-crash.log, which would otherwise be lost.
func runAsService() (bool, error) {
	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		return false, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return true, err
	}
	if err = os.Chdir(filepath.Dir(exe)); err != nil {
		return true, err
	}
	if err = os.MkdirAll("logs", 0700); err != nil {
		return true, err
	}
	log, err := openRotating(filepath.Join("logs", "goomni-service.log"))
	if err != nil {
		return true, err
	}
	defer log.Close()
	crashPath := filepath.Join("logs", "goomni-crash.log")
	rotateIfLarge(crashPath)
	crash, err := os.OpenFile(crashPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return true, err
	}
	defer crash.Close()
	if err = debug.SetCrashOutput(crash, debug.CrashOptions{}); err != nil {
		return true, err
	}
	logrus.SetOutput(log)
	os.Stdout, os.Stderr = crash, crash
	h := &serviceHandler{}
	if err = svc.Run("", h); err != nil { // the name is ignored for an own-process service
		return true, err
	}
	return true, h.err
}

type serviceHandler struct{ err error }

// Execute runs the command line the service was registered with (normally
// `rest`) with a context the SCM's stop/shutdown request cancels, which is
// the same graceful path as Ctrl+C in a console. An error exit returns a
// non-zero code so the service's recovery actions restart it.
func (h *serviceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- NewCommand().ExecuteContext(ctx) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			h.err = err
			if err != nil {
				logrus.WithError(err).Error("GoOmni stopped with an error")
				return true, 1
			}
			return false, 0
		case r := <-requests:
			switch r.Cmd {
			case svc.Interrogate:
				status <- r.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending, WaitHint: uint32(stopTimeout / time.Millisecond)}
				cancel()
				select {
				case h.err = <-done:
				case <-time.After(stopTimeout):
					logrus.Error("GoOmni did not finish its shutdown in time")
				}
				return false, 0
			}
		}
	}
}

// logMaxBytes caps each service log file; one previous file (<name>.1) is kept.
const logMaxBytes = 10 << 20

// rotatingFile is the service log. Docker and systemd rotate a process's
// output; the SCM does not, so when the file would pass logMaxBytes it is
// renamed to <name>.1 (replacing the older one) and a new file is started.
type rotatingFile struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

func openRotating(path string) (*rotatingFile, error) {
	r := &rotatingFile{path: path}
	return r, r.open()
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, info.Size()
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size > 0 && r.size+int64(len(p)) > logMaxBytes {
		r.f.Close()
		_ = os.Rename(r.path, r.path+".1")
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}

// rotateIfLarge bounds the crash log across restarts. It is written only when
// the process dies, so a check at start is enough.
func rotateIfLarge(path string) {
	if info, err := os.Stat(path); err == nil && info.Size() > logMaxBytes {
		_ = os.Rename(path, path+".1")
	}
}
