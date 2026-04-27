// Package mcp implements an MCP server that exposes pixels sandbox
// lifecycle, exec, and file I/O as tools for AI agents.
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Sandbox is a single tracked MCP-managed sandbox.
type Sandbox struct {
	Name           string    `json:"name"`
	Label          string    `json:"label,omitempty"`
	Image          string    `json:"image"`
	IP             string    `json:"ip,omitempty"`
	Status         string    `json:"status"` // "running" | "stopped"
	CreatedAt      time.Time `json:"created_at"`
	LastActivityAt time.Time `json:"last_activity_at"`
}

// State is the in-memory + on-disk MCP state.
type State struct {
	path string
	mu   sync.RWMutex
	data stateData
}

type stateData struct {
	Sandboxes []Sandbox `json:"sandboxes"`
}

// LoadState reads state from path. Missing or corrupt files yield an empty state.
func LoadState(path string) (*State, error) {
	s := &State{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading state: %w", err)
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		fmt.Fprintf(os.Stderr, "pixels mcp: state file corrupt, starting empty: %v\n", err)
		s.data = stateData{}
	}
	return s, nil
}

// Sandboxes returns a copy of the current sandbox slice.
func (s *State) Sandboxes() []Sandbox {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Sandbox, len(s.data.Sandboxes))
	copy(out, s.data.Sandboxes)
	return out
}

// Get returns a sandbox by name and whether it was found.
func (s *State) Get(name string) (Sandbox, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sb := range s.data.Sandboxes {
		if sb.Name == name {
			return sb, true
		}
	}
	return Sandbox{}, false
}

// Add inserts or replaces a sandbox.
func (s *State) Add(sb Sandbox) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.data.Sandboxes {
		if existing.Name == sb.Name {
			s.data.Sandboxes[i] = sb
			return
		}
	}
	s.data.Sandboxes = append(s.data.Sandboxes, sb)
}

// Remove deletes a sandbox by name. No-op if not present.
func (s *State) Remove(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.data.Sandboxes[:0]
	for _, sb := range s.data.Sandboxes {
		if sb.Name != name {
			out = append(out, sb)
		}
	}
	s.data.Sandboxes = out
}

// BumpActivity advances last_activity_at for the given sandbox. No-op if missing.
func (s *State) BumpActivity(name string, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Sandboxes {
		if s.data.Sandboxes[i].Name == name {
			s.data.Sandboxes[i].LastActivityAt = t
			return
		}
	}
}

// SetStatus updates a sandbox's status. No-op if missing.
func (s *State) SetStatus(name, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Sandboxes {
		if s.data.Sandboxes[i].Name == name {
			s.data.Sandboxes[i].Status = status
			return
		}
	}
}

// Save persists state atomically: write to <path>.tmp, then rename.
func (s *State) Save() error {
	s.mu.RLock()
	b, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write tmp state: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename state: %w", err)
	}
	return nil
}
