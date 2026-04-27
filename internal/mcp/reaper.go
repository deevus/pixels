package mcp

import (
	"context"
	"fmt"
	"os"
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
	IdleStopAfter    time.Duration
	HardDestroyAfter time.Duration
	Now              func() time.Time // injectable clock for tests
}

// Tick performs one reaper pass. Safe to call repeatedly.
func (r *Reaper) Tick(ctx context.Context) {
	if r.Now == nil {
		r.Now = time.Now
	}
	now := r.Now()

	for _, sb := range r.State.Sandboxes() {
		if now.Sub(sb.CreatedAt) > r.HardDestroyAfter {
			if err := r.Backend.Delete(ctx, sb.Name); err != nil {
				fmt.Fprintf(os.Stderr, "pixels mcp: destroy %s: %v\n", sb.Name, err)
				continue
			}
			r.State.Remove(sb.Name)
			continue
		}
		if sb.Status == "running" && now.Sub(sb.LastActivityAt) > r.IdleStopAfter {
			if err := r.Backend.Stop(ctx, sb.Name); err != nil {
				fmt.Fprintf(os.Stderr, "pixels mcp: stop %s: %v\n", sb.Name, err)
				continue
			}
			r.State.SetStatus(sb.Name, "stopped")
		}
	}

	if err := r.State.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "pixels mcp: save state: %v\n", err)
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
