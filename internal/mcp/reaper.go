package mcp

import (
	"context"
	"log/slog"
	"time"
)

// LifecycleBackend is the subset of sandbox.Backend that the reaper needs.
type LifecycleBackend interface {
	Stop(ctx context.Context, name string) error
	Delete(ctx context.Context, name string) error
}

// Reaper enforces idle-stop and hard-destroy TTLs on tracked sandboxes.
type Reaper struct {
	State            *State
	Backend          LifecycleBackend
	Locks            *SandboxLocks // shared with Tools; TryLock to skip busy sandboxes
	IdleStopAfter    time.Duration
	HardDestroyAfter time.Duration
	Log              *slog.Logger
	Now              func() time.Time // injectable clock for tests
}

func (r *Reaper) log() *slog.Logger {
	if r.Log == nil {
		return NopLogger()
	}
	return r.Log
}

func (r *Reaper) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Tick performs one reaper pass. Safe to call repeatedly.
func (r *Reaper) Tick(ctx context.Context) {
	now := r.now()
	for _, sb := range r.State.Sandboxes() {
		r.tickOne(ctx, sb, now)
	}
	if err := r.State.Save(); err != nil {
		r.log().Error("save state during reap", "err", err)
	}
}

func (r *Reaper) tickOne(ctx context.Context, sb Sandbox, now time.Time) {
	if r.Locks != nil {
		m := r.Locks.For(sb.Name)
		if !m.TryLock() {
			r.log().Debug("reaper skipped busy sandbox", "name", sb.Name)
			return
		}
		defer m.Unlock()
	}
	r.applyTTL(ctx, sb, now)
}

func (r *Reaper) applyTTL(ctx context.Context, sb Sandbox, now time.Time) {
	// Don't reap during provisioning — the goroutine is still working.
	if sb.Status == "provisioning" {
		return
	}
	if now.Sub(sb.CreatedAt) > r.HardDestroyAfter {
		if err := r.Backend.Delete(ctx, sb.Name); err != nil {
			r.log().Error("destroy", "name", sb.Name, "err", err)
			return
		}
		r.State.Remove(sb.Name)
		return
	}
	if sb.Status == "running" && now.Sub(sb.LastActivityAt) > r.IdleStopAfter {
		if err := r.Backend.Stop(ctx, sb.Name); err != nil {
			r.log().Error("stop", "name", sb.Name, "err", err)
			return
		}
		r.State.SetStatus(sb.Name, "stopped")
	}
}

// Run starts a ticker that calls Tick until ctx is cancelled.
func (r *Reaper) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Tick(ctx)
		}
	}
}
