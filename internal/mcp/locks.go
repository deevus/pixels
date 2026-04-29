package mcp

import "sync"

// SandboxLocks provides one mutex per sandbox name. Tool handlers acquire
// the lock for the duration of any container-touching call so concurrent
// ops on a single sandbox do not race; the reaper uses TryLock to skip
// busy sandboxes.
//
// Locks are created on first access and never pruned. Each entry is a
// few bytes; sandbox count is bounded by user activity. If memory growth
// becomes a concern (it has not, by orders of magnitude), add refcounted
// deletion on Destroy — out of scope for v1.
type SandboxLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// For returns the mutex for the given sandbox name, creating it on first
// access. The caller is responsible for Lock/Unlock. Safe for concurrent use.
func (l *SandboxLocks) For(name string) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.locks == nil {
		l.locks = make(map[string]*sync.Mutex)
	}
	m, ok := l.locks[name]
	if !ok {
		m = &sync.Mutex{}
		l.locks[name] = m
	}
	return m
}

// Acquire locks the sandbox's mutex and returns its Unlock function, intended
// for the idiom `defer t.Locks.Acquire(name)()` in handlers that hold the lock
// for the entire call.
func (l *SandboxLocks) Acquire(name string) func() {
	m := l.For(name)
	m.Lock()
	return m.Unlock
}
