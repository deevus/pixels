package mcp

import (
	"errors"
	"os"
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

func TestStateSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s, _ := LoadState(path)
	s.Add(Sandbox{
		Name:      "px-mcp-1",
		Image:     "ubuntu/24.04",
		Status:    "running",
		CreatedAt: time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC),
	})
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s2, err := LoadState(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := s2.Get("px-mcp-1")
	if !ok || got.Image != "ubuntu/24.04" {
		t.Fatalf("reloaded sandbox = %+v, ok=%v", got, ok)
	}
}

func TestStateSaveAtomicNoTmpLeft(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s, _ := LoadState(path)
	s.Add(Sandbox{Name: "a"})
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "state.json" {
			t.Errorf("unexpected leftover file in state dir: %q", e.Name())
		}
	}
}

func TestStateLoadCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadState(path)
	if err != nil {
		t.Fatalf("expected silent recovery, got error: %v", err)
	}
	if got := len(s.Sandboxes()); got != 0 {
		t.Errorf("Sandboxes = %d, want 0 after corrupt load", got)
	}
}

func TestStateMarkProvisioning(t *testing.T) {
	dir := t.TempDir()
	s, _ := LoadState(filepath.Join(dir, "s.json"))
	now := time.Now().UTC()
	s.Add(Sandbox{
		Name:           "p",
		Status:         "provisioning",
		CreatedAt:      now,
		LastActivityAt: now,
	})

	s.MarkRunning("p")
	got, _ := s.Get("p")
	if got.Status != "running" {
		t.Errorf("status = %q, want running", got.Status)
	}

	s.MarkFailed("p", errors.New("boom"))
	got, _ = s.Get("p")
	if got.Status != "failed" {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if got.Error != "boom" {
		t.Errorf("error = %q, want boom", got.Error)
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
