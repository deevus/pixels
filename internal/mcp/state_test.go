package mcp

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStateLoadEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := len(s.Sandboxes()); got != 0 {
		t.Errorf("Sandboxes = %d, want 0", got)
	}
}

func TestStateAddAndGet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s, _ := LoadState(path)
	now := time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC)
	s.Add(Sandbox{
		Name:           "px-mcp-abc123",
		Image:          "ubuntu/24.04",
		IP:             "10.0.0.42",
		Status:         "running",
		CreatedAt:      now,
		LastActivityAt: now,
	})

	got, ok := s.Get("px-mcp-abc123")
	if !ok {
		t.Fatal("sandbox not found")
	}
	if got.Image != "ubuntu/24.04" {
		t.Errorf("Image = %q, want ubuntu/24.04", got.Image)
	}
}

func TestStateRemove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s, _ := LoadState(path)
	s.Add(Sandbox{Name: "a"})
	s.Add(Sandbox{Name: "b"})
	s.Remove("a")

	if _, ok := s.Get("a"); ok {
		t.Error("a should be removed")
	}
	if _, ok := s.Get("b"); !ok {
		t.Error("b should remain")
	}
}

func TestStateBumpActivity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s, _ := LoadState(path)

	s.Add(Sandbox{
		Name:           "x",
		LastActivityAt: time.Now().Add(-1 * time.Hour),
	})
	before, _ := s.Get("x")
	s.BumpActivity("x", time.Now())
	after, _ := s.Get("x")

	if !after.LastActivityAt.After(before.LastActivityAt) {
		t.Error("LastActivityAt did not advance")
	}
}
