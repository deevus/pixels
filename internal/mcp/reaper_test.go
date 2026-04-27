package mcp

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeBackend struct {
	mu      sync.Mutex
	stopped []string
	deleted []string
	stopErr error
	delErr  error
}

func (b *fakeBackend) Stop(ctx context.Context, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopped = append(b.stopped, name)
	return b.stopErr
}
func (b *fakeBackend) Delete(ctx context.Context, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deleted = append(b.deleted, name)
	return b.delErr
}

func TestReaperStopsIdle(t *testing.T) {
	dir := t.TempDir()
	s, _ := LoadState(filepath.Join(dir, "s.json"))
	now := time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC)
	s.Add(Sandbox{
		Name:           "idle",
		Status:         "running",
		CreatedAt:      now.Add(-2 * time.Hour),
		LastActivityAt: now.Add(-90 * time.Minute),
	})
	s.Add(Sandbox{
		Name:           "active",
		Status:         "running",
		CreatedAt:      now.Add(-2 * time.Hour),
		LastActivityAt: now.Add(-10 * time.Minute),
	})

	be := &fakeBackend{}
	r := &Reaper{
		State:            s,
		Backend:          be,
		IdleStopAfter:    1 * time.Hour,
		HardDestroyAfter: 24 * time.Hour,
		Now:              func() time.Time { return now },
	}
	r.Tick(context.Background())

	if len(be.stopped) != 1 || be.stopped[0] != "idle" {
		t.Errorf("stopped = %v, want [idle]", be.stopped)
	}
	got, _ := s.Get("idle")
	if got.Status != "stopped" {
		t.Errorf("idle status = %q, want stopped", got.Status)
	}
}

func TestReaperSkipsBusySandbox(t *testing.T) {
	dir := t.TempDir()
	s, _ := LoadState(filepath.Join(dir, "s.json"))
	now := time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC)
	s.Add(Sandbox{
		Name:           "busy",
		Status:         "running",
		CreatedAt:      now.Add(-2 * time.Hour),
		LastActivityAt: now.Add(-90 * time.Minute), // would normally be stopped
	})

	be := &fakeBackend{}
	locks := &SandboxLocks{}
	// Hold the per-sandbox lock to simulate an in-flight tool call.
	m := locks.For("busy")
	m.Lock()
	defer m.Unlock()

	r := &Reaper{
		State:            s,
		Backend:          be,
		Locks:            locks,
		IdleStopAfter:    1 * time.Hour,
		HardDestroyAfter: 24 * time.Hour,
		Now:              func() time.Time { return now },
	}
	r.Tick(context.Background())

	if len(be.stopped) != 0 {
		t.Errorf("reaper should have skipped busy sandbox; stopped=%v", be.stopped)
	}
}

func TestReaperContinuesAfterStopError(t *testing.T) {
	dir := t.TempDir()
	s, _ := LoadState(filepath.Join(dir, "s.json"))
	now := time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC)
	s.Add(Sandbox{Name: "fail", Status: "running", CreatedAt: now.Add(-2 * time.Hour), LastActivityAt: now.Add(-90 * time.Minute)})
	s.Add(Sandbox{Name: "ok", Status: "running", CreatedAt: now.Add(-2 * time.Hour), LastActivityAt: now.Add(-90 * time.Minute)})

	be := &fakeBackend{}
	be.stopErr = errors.New("backend gone")
	r := &Reaper{
		State:            s,
		Backend:          be,
		Locks:            &SandboxLocks{},
		IdleStopAfter:    1 * time.Hour,
		HardDestroyAfter: 24 * time.Hour,
		Now:              func() time.Time { return now },
	}
	r.Tick(context.Background())

	// Both sandboxes should still be in state with status "running" — neither
	// stopped successfully, but the reaper should have logged and moved on.
	if got := s.Sandboxes(); len(got) != 2 {
		t.Errorf("len(state) = %d, want 2", len(got))
	}
}

func TestReaperDestroysOld(t *testing.T) {
	dir := t.TempDir()
	s, _ := LoadState(filepath.Join(dir, "s.json"))
	now := time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC)
	s.Add(Sandbox{
		Name:           "ancient",
		Status:         "stopped",
		CreatedAt:      now.Add(-25 * time.Hour),
		LastActivityAt: now.Add(-25 * time.Hour),
	})

	be := &fakeBackend{}
	r := &Reaper{
		State:            s,
		Backend:          be,
		IdleStopAfter:    1 * time.Hour,
		HardDestroyAfter: 24 * time.Hour,
		Now:              func() time.Time { return now },
	}
	r.Tick(context.Background())

	if len(be.deleted) != 1 || be.deleted[0] != "ancient" {
		t.Errorf("deleted = %v, want [ancient]", be.deleted)
	}
	if _, ok := s.Get("ancient"); ok {
		t.Error("ancient should be removed from state")
	}
}
