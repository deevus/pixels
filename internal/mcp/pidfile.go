package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
)

// PIDFile is an acquired single-instance lock backed by a pidfile.
// The lock is held via flock(2); the kernel releases it automatically when
// the holder process exits, so there is no stale-PID state to detect.
type PIDFile struct {
	lock *flock.Flock
}

// AcquirePIDFile takes a non-blocking exclusive flock on path, then writes
// the holder's PID. If another live process holds the lock, the existing
// PID is included in the error when readable.
func AcquirePIDFile(path string) (*PIDFile, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create pidfile dir: %w", err)
	}

	lock := flock.New(path)
	locked, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("flock pidfile %s: %w", path, err)
	}
	if !locked {
		existing, _ := os.ReadFile(path)
		pidStr := strings.TrimSpace(string(existing))
		if pidStr == "" {
			return nil, fmt.Errorf("another pixels mcp is running (pidfile=%s)", path)
		}
		return nil, fmt.Errorf("another pixels mcp is running (pid=%s, pidfile=%s)", pidStr, path)
	}

	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		_ = lock.Unlock()
		return nil, fmt.Errorf("write pidfile %s: %w", path, err)
	}

	return &PIDFile{lock: lock}, nil
}

// Release drops the flock. The pidfile is intentionally left on disk — the
// kernel flock is the source of truth for liveness, and removing the file
// could race with a successor process that has already taken the lock and
// written its own PID. A successor's TryLock + truncate-and-write naturally
// overwrites any stale content. Safe to call multiple times.
func (p *PIDFile) Release() {
	if p.lock == nil {
		return
	}
	_ = p.lock.Unlock()
	p.lock = nil
}
