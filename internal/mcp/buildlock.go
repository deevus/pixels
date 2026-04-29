package mcp

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

// BuildLock is a file-level exclusive lock for serialising base builds
// across the daemon and CLI processes.
type BuildLock struct {
	lock *flock.Flock
}

// AcquireBuildLock takes an exclusive flock on <dir>/builds/<name>.lock.
// Blocks if another process holds the lock for the same name.
func AcquireBuildLock(dir, name string) (*BuildLock, error) {
	lockDir := filepath.Join(dir, "builds")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		return nil, fmt.Errorf("create build lock dir: %w", err)
	}
	path := filepath.Join(lockDir, name+".lock")
	lock := flock.New(path)
	if err := lock.Lock(); err != nil {
		return nil, fmt.Errorf("flock %s: %w", path, err)
	}
	return &BuildLock{lock: lock}, nil
}

// Release drops the file lock. Safe to call multiple times.
func (l *BuildLock) Release() {
	if l.lock == nil {
		return
	}
	_ = l.lock.Unlock()
	l.lock = nil
}
