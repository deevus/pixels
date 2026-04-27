package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// PIDFile represents an acquired single-instance lock backed by a pidfile.
type PIDFile struct {
	path string
}

// AcquirePIDFile creates path with the current PID. Fails if path exists and
// the recorded PID is alive. Stale PIDs are overwritten.
func AcquirePIDFile(path string) (*PIDFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create pidfile dir: %w", err)
	}

	if existing, err := os.ReadFile(path); err == nil {
		pidStr := strings.TrimSpace(string(existing))
		if pid, err := strconv.Atoi(pidStr); err == nil && pidAlive(pid) {
			return nil, fmt.Errorf("another pixels mcp is running (pid=%d, pidfile=%s)", pid, path)
		}
	}

	content := fmt.Sprintf("%d\n", os.Getpid())
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return nil, fmt.Errorf("write pidfile: %w", err)
	}
	return &PIDFile{path: path}, nil
}

// Release removes the pidfile. Safe to call multiple times.
func (p *PIDFile) Release() error {
	err := os.Remove(p.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// pidAlive reports whether a process with the given pid exists.
// On Unix, signal 0 is the standard "is this PID alive" check.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
