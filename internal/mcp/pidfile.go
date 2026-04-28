package mcp

import (
	"errors"
	"fmt"
	"io/fs"
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

// acquireMaxAttempts bounds the steal-stale-pidfile retry loop. Each attempt
// either publishes a fresh pidfile or observes a live owner; pathological
// thrashing (multiple acquirers racing on a stale pidfile) terminates here
// rather than spinning.
const acquireMaxAttempts = 8

// AcquirePIDFile publishes a pidfile atomically using a write-tmp-then-link
// pattern: each acquirer writes its PID to a unique tempfile, then attempts
// os.Link(tmp, path). os.Link is atomic and fails with EEXIST if path exists,
// guaranteeing that observers of `path` always see a fully-written PID — no
// "empty file" window for racers to misinterpret as stale.
//
// On EEXIST: read the existing PID, return an error if it's alive; otherwise
// remove path and retry. The os.SameFile guard before Remove prevents racing
// acquirers from unlinking each other's freshly-published pidfile.
func AcquirePIDFile(path string) (*PIDFile, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create pidfile dir: %w", err)
	}

	// Pre-write our PID to a unique tempfile. This file is the source of the
	// hardlink we'll publish at `path`; whoever links first wins atomically.
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return nil, fmt.Errorf("create pidfile tmp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // safe: after Link, the inode survives via path's reference

	if _, err := fmt.Fprintf(tmp, "%d\n", os.Getpid()); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("write pidfile tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close pidfile tmp: %w", err)
	}

	for attempt := 0; attempt < acquireMaxAttempts; attempt++ {
		if err := os.Link(tmpName, path); err == nil {
			return &PIDFile{path: path}, nil
		} else if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("link pidfile: %w", err)
		}

		// EEXIST: capture the existing file's identity and decide.
		stBefore, err := os.Stat(path)
		if err != nil {
			continue // raced with a remover; retry
		}
		existing, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		pidStr := strings.TrimSpace(string(existing))
		if pid, perr := strconv.Atoi(pidStr); perr == nil && pidAlive(pid) {
			return nil, fmt.Errorf("another pixels mcp is running (pid=%d, pidfile=%s)", pid, path)
		}

		// Stale or unparseable. Only unlink if the inode is unchanged —
		// otherwise another acquirer has already replaced it and we'd
		// delete their live lock.
		stAfter, err := os.Stat(path)
		if err != nil {
			continue
		}
		if !os.SameFile(stBefore, stAfter) {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("remove stale pidfile: %w", err)
		}
		// Loop and retry the link.
	}
	return nil, fmt.Errorf("acquire pidfile %s: too many race retries", path)
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
