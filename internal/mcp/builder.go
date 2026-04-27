package mcp

import (
	"context"
	"sync"
	"time"
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

	mu       sync.Mutex
	builds   map[string]*buildState
	failures map[string]failureEntry
}

type buildState struct {
	done chan struct{}
	err  error
}

type failureEntry struct {
	err   error
	until time.Time
}

// Build is the deduplicated entrypoint. Concurrent callers for the same
// name share a single DoBuild invocation; the result is delivered to all.
func (b *Builder) Build(ctx context.Context, name string) error {
	b.mu.Lock()
	if b.builds == nil {
		b.builds = make(map[string]*buildState)
	}
	if b.failures == nil {
		b.failures = make(map[string]failureEntry)
	}

	if fe, ok := b.failures[name]; ok && time.Now().Before(fe.until) {
		b.mu.Unlock()
		return fe.err
	}

	if bs, ok := b.builds[name]; ok {
		b.mu.Unlock()
		select {
		case <-bs.done:
			return bs.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	bs := &buildState{done: make(chan struct{})}
	b.builds[name] = bs
	b.mu.Unlock()

	err := b.DoBuild(ctx, name)

	b.mu.Lock()
	bs.err = err
	delete(b.builds, name)
	if err != nil && b.FailureTTL > 0 {
		b.failures[name] = failureEntry{err: err, until: time.Now().Add(b.FailureTTL)}
	} else {
		delete(b.failures, name)
	}
	b.mu.Unlock()

	close(bs.done)
	return err
}

// Status returns the current state for name:
//   "building" — a DoBuild is in flight
//   "failed"   — a recent build failed and is still cached; err is non-nil
//   ""         — neither in flight nor cached; caller checks snapshot existence
//                to distinguish "ready" from "missing"
func (b *Builder) Status(name string) (status string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.builds[name]; ok {
		return "building", nil
	}
	if fe, ok := b.failures[name]; ok && time.Now().Before(fe.until) {
		return "failed", fe.err
	}
	return "", nil
}
