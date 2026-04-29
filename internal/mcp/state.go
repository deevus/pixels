package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Sandbox is a single tracked MCP-managed sandbox.
type Sandbox struct {
	Name           string    `json:"name"`
	Label          string    `json:"label,omitempty"`
	Image          string    `json:"image"`
	Base           string    `json:"base,omitempty"` // name of the base, if cloned
	IP             string    `json:"ip,omitempty"`
	Status         string    `json:"status"`          // "provisioning" | "running" | "stopped" | "failed"
	Error          string    `json:"error,omitempty"` // populated when status=failed
	CreatedAt      time.Time `json:"created_at"`
	LastActivityAt time.Time `json:"last_activity_at"`
}

// State is the in-memory + on-disk MCP state.
type State struct {
	path      string
	mu        sync.RWMutex
	sandboxes map[string]Sandbox
	log       *slog.Logger
}

// SetLogger assigns the logger after construction. Call once at startup.
func (s *State) SetLogger(l *slog.Logger) { s.log = l }

// SetPathForTest sets the state file path for testing only.
func (s *State) SetPathForTest(p string) { s.path = p }

// stateData is the on-disk JSON wire format.
type stateData struct {
	Sandboxes []Sandbox `json:"sandboxes"`
}

// LoadState reads state from path. Missing or corrupt files yield an empty state.
func LoadState(path string) (*State, error) {
	s := &State{path: path, sandboxes: make(map[string]Sandbox)}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading state: %w", err)
	}
	var data stateData
	if err := json.Unmarshal(b, &data); err != nil {
		fmt.Fprintf(os.Stderr, "pixels mcp: state file corrupt, starting empty: %v\n", err)
		return s, nil
	}
	for _, sb := range data.Sandboxes {
		s.sandboxes[sb.Name] = sb
	}
	return s, nil
}

// Sandboxes returns a copy of the current sandboxes. Order is undefined.
func (s *State) Sandboxes() []Sandbox {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Collect(maps.Values(s.sandboxes))
}

// Get returns a sandbox by name and whether it was found.
func (s *State) Get(name string) (Sandbox, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sb, ok := s.sandboxes[name]
	return sb, ok
}

// Add inserts or replaces a sandbox.
func (s *State) Add(sb Sandbox) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sandboxes[sb.Name] = sb
}

// Remove deletes a sandbox by name. No-op if not present.
func (s *State) Remove(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sandboxes, name)
}

// update applies fn to the named sandbox under the write lock. No-op if missing.
func (s *State) update(name string, fn func(*Sandbox)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sb, ok := s.sandboxes[name]
	if !ok {
		return
	}
	fn(&sb)
	s.sandboxes[name] = sb
}

// BumpActivity advances last_activity_at for the given sandbox. No-op if missing.
func (s *State) BumpActivity(name string, t time.Time) {
	s.update(name, func(sb *Sandbox) { sb.LastActivityAt = t })
}

// MarkRunning transitions a sandbox to "running" and clears any prior error.
func (s *State) MarkRunning(name string) {
	s.update(name, func(sb *Sandbox) {
		sb.Status = "running"
		sb.Error = ""
	})
}

// SetIP updates a sandbox's IP address. No-op if missing.
func (s *State) SetIP(name, ip string) {
	s.update(name, func(sb *Sandbox) { sb.IP = ip })
}

// MarkFailed transitions a sandbox to "failed" and records the error message.
func (s *State) MarkFailed(name string, err error) {
	s.update(name, func(sb *Sandbox) {
		sb.Status = "failed"
		if err != nil {
			sb.Error = err.Error()
		}
	})
}

// SetStatus updates a sandbox's status. No-op if missing.
func (s *State) SetStatus(name, status string) {
	s.update(name, func(sb *Sandbox) { sb.Status = status })
}

// Save persists state atomically: write to <path>.tmp with fsync, then rename.
// Without the fsync, a crash after rename can leave state.json zero-length on
// ext4/xfs default mount options. On-disk sandbox order is non-deterministic
// across saves (map iteration order).
func (s *State) Save() error {
	s.mu.RLock()
	data := stateData{Sandboxes: slices.Collect(maps.Values(s.sandboxes))}
	s.mu.RUnlock()
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open tmp state: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write tmp state: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync tmp state: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close tmp state: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename state: %w", err)
	}
	// Best-effort: fsync the parent dir so the rename itself is durable.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
