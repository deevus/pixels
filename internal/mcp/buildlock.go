package mcp

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// BuildLock is a file-level exclusive lock for serialising base builds
// across the daemon and CLI processes.
type BuildLock struct {
	f *os.File
}

// AcquireBuildLock takes an exclusive flock on <dir>/builds/<name>.lock.
// Blocks if another process holds the lock for the same name.
func AcquireBuildLock(dir, name string) (*BuildLock, error) {
	lockDir := filepath.Join(dir, "builds")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		return nil, fmt.Errorf("create build lock dir: %w", err)
	}
	path := filepath.Join(lockDir, name+".lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open build lock %s: %w", path, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("flock %s: %w", path, err)
	}
	return &BuildLock{f: f}, nil
}

// Release drops the file lock. Safe to call multiple times.
func (l *BuildLock) Release() {
	if l.f == nil {
		return
	}
	_ = unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}
