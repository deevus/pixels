package mcp

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Builder coordinates concurrent in-process builds of named bases. It
// dedupes simultaneous callers (only one DoBuild runs per name) and caches
// recent failures so repeated callers don't burn cycles re-failing.
//
// Cross-process serialisation against the CLI is provided by BuildLock,
// which DoBuild itself acquires inside its body.
type Builder struct {
	// DoBuild performs the actual build. Set by the caller. Must be safe
	// to call from a goroutine.
	DoBuild func(ctx context.Context, name string) error

	// FailureTTL is how long a failed build is cached before retrying.
	// Zero means failures aren't cached.
	FailureTTL time.Duration

	group singleflight.Group

	mu       sync.Mutex
	inflight map[string]struct{}
	failures map[string]failureEntry
}

type failureEntry struct {
	err   error
	until time.Time
}

// Build is the deduplicated entrypoint. Concurrent callers for the same
// name share a single DoBuild invocation; the result is delivered to all.
func (b *Builder) Build(ctx context.Context, name string) error {
	b.mu.Lock()
	if fe, ok := b.failures[name]; ok && time.Now().Before(fe.until) {
		b.mu.Unlock()
		return fe.err
	}
	if b.inflight == nil {
		b.inflight = make(map[string]struct{})
	}
	b.inflight[name] = struct{}{}
	b.mu.Unlock()

	ch := b.group.DoChan(name, func() (interface{}, error) {
		err := b.DoBuild(ctx, name)

		b.mu.Lock()
		delete(b.inflight, name)
		if err != nil && b.FailureTTL > 0 {
			if b.failures == nil {
				b.failures = make(map[string]failureEntry)
			}
			b.failures[name] = failureEntry{err: err, until: time.Now().Add(b.FailureTTL)}
		} else {
			delete(b.failures, name)
		}
		b.mu.Unlock()

		return nil, err
	})

	select {
	case r := <-ch:
		return r.Err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Status returns the current state for name:
//
//	"building" — a DoBuild is in flight
//	"failed"   — a recent build failed and is still cached; err is non-nil
//	""         — neither in flight nor cached; caller checks snapshot existence
//	             to distinguish "ready" from "missing"
func (b *Builder) Status(name string) (status string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.inflight[name]; ok {
		return "building", nil
	}
	if fe, ok := b.failures[name]; ok && time.Now().Before(fe.until) {
		return "failed", fe.err
	}
	return "", nil
}
