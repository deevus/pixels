# MCP Async Create + Base-Pixel Lifecycle Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make `create_sandbox` async (returns immediately with `provisioning` status) and add a curated **base-pixel** layer — pre-built ZFS snapshots that `create_sandbox(base="X")` clones from. Building bases is invisible to the agent: a missing base triggers an in-flight build during `create_sandbox`. The CLI exposes the same build operation directly for user-driven pre-warming.

**Architecture:** A single goroutine per `create_sandbox` call owns the work. If a base is requested and the snapshot is missing, the goroutine acquires a per-base build coordinator (`Builder`) that dedupes concurrent in-process callers and a cross-process file lock (`BuildLock`) that serializes against CLI builds. Build state is observable via `list_bases`. State gains two new statuses (`provisioning`, `failed`) and a per-sandbox `Error` field.

**Tech Stack:**
- Go 1.25 (`log/slog`, `context`, `sync`, `golang.org/x/sys/unix.Flock`)
- existing: `internal/mcp/`, `internal/config/`, `sandbox/`, `cmd/`
- no new dependencies (stdlib `golang.org/x/sys` is already an indirect dep)

**Worktree:** Same worktree as Plan A: `/Users/sh/Projects/pixels/.worktrees/mcp-sandbox` on `feat/mcp-sandbox-server`.

**Prerequisite:** Plan A (`docs/plans/2026-04-27-mcp-hardening.md`) must land first. Specifically Task 1 (slog logger) and Task 2 (`SandboxLocks`) — this plan's provisioning goroutine and Builder both rely on them.

**VCS:** Same as Plan A: `jj describe -m "..." && jj new` per task.

---

## Concurrency design

This plan introduces three new concurrency primitives. Together with the two from Plan A, the package has five. Lock acquisition order (top down; never reverse):

```
LOCK ACQUISITION ORDER:

  1. SandboxLocks.For(name)  — long-held; per-sandbox container ops, including the
                               provisioning goroutine and clone-from-base path.
  2. Builder.mu              — short-held; protects build state map + failure cache.
  3. State.mu                — short-held; protects in-memory state. NEVER held
                               across backend I/O.

INDEPENDENT PRIMITIVES (do not interact with the above):

  - BuildLock (file flock)   — only acquired inside Builder.doBuild;
                               serialises CLI vs daemon builds of the same base.
  - PIDFile                  — held for daemon lifetime.

INVARIANTS:

  - sync.Mutex is non-reentrant. Never re-acquire a lock you already hold.
  - Reaper.Tick uses TryLock on SandboxLocks. It never blocks.
  - The provisioning goroutine acquires SandboxLocks.For(newName) ITSELF
    (does not inherit a held lock from the request goroutine), and re-checks
    state at the top to handle the destroy-during-create race window.
  - Builder.Build dedupes concurrent in-process callers via a buildState map;
    cross-process serialization is via BuildLock (flock) inside doBuild.
  - Builder.doBuild's lock is acquired INSIDE the singleflight-equivalent —
    so only one in-process goroutine ever holds the file lock at a time.
```

A copy of this block goes into `internal/mcp/doc.go` as the package's concurrency contract. Engineers editing this package read it before touching a lock.

---

## Engineer-orientation notes

Before starting:

1. **Verify Plan A is fully merged** — specifically `SandboxLocks` and the `slog` logger. Run:
   ```
   grep -n "SandboxLocks" internal/mcp/locks.go
   grep -n "NewLogger" internal/mcp/log.go
   ```
   Both should hit. If not, finish Plan A first.
2. **Read `sandbox/sandbox.go`** for the `Backend.CreateSnapshot` / `ListSnapshots` / `DeleteSnapshot` / `CloneFrom` / `Ready` signatures. The build implementation calls into all of them.
3. **Read `internal/mcp/tools.go` `CreateSandbox`** — Plan A may have left it synchronous; Task 1 here makes it async.
4. **Read `sandbox/filesexec.go`** — the build process uses `WriteFile` to upload the setup script.

**Test conventions:** same as Plan A.

---

## Task 1: Add `provisioning` and `failed` status, plus `Error` field on Sandbox

**Files:**
- Modify: `internal/mcp/state.go` — add `Error string` field, new helper methods.
- Modify: `internal/mcp/state_test.go`

**Step 1: Write the failing test**

Append to `internal/mcp/state_test.go`:

```go
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
```

(Add `"errors"` import.)

**Step 2: Verify failure**

```
go test ./internal/mcp -run TestStateMarkProvisioning -v
```

Expected: FAIL — methods undefined.

**Step 3: Implement**

In `internal/mcp/state.go`:

Add `Error` field to the `Sandbox` struct:

```go
type Sandbox struct {
	Name           string    `json:"name"`
	Label          string    `json:"label,omitempty"`
	Image          string    `json:"image"`
	Base           string    `json:"base,omitempty"`            // NEW — name of the base, if cloned
	IP             string    `json:"ip,omitempty"`
	Status         string    `json:"status"` // "provisioning" | "running" | "stopped" | "failed"
	Error          string    `json:"error,omitempty"`           // NEW — populated when status=failed
	CreatedAt      time.Time `json:"created_at"`
	LastActivityAt time.Time `json:"last_activity_at"`
}
```

Add helper methods:

```go
// MarkRunning transitions a sandbox to "running" and clears any prior error.
func (s *State) MarkRunning(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Sandboxes {
		if s.data.Sandboxes[i].Name == name {
			s.data.Sandboxes[i].Status = "running"
			s.data.Sandboxes[i].Error = ""
			return
		}
	}
}

// MarkFailed transitions a sandbox to "failed" and records the error message.
func (s *State) MarkFailed(name string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Sandboxes {
		if s.data.Sandboxes[i].Name == name {
			s.data.Sandboxes[i].Status = "failed"
			if err != nil {
				s.data.Sandboxes[i].Error = err.Error()
			}
			return
		}
	}
}
```

**Step 4: Run + commit**

```
go test ./internal/mcp -v
```

```bash
jj describe -m "feat(mcp): add provisioning/failed status and Error field on Sandbox"
jj new
```

---

## Task 2: Refactor `CreateSandbox` to async with provisioning goroutine

**Files:**
- Modify: `internal/mcp/tools.go` (CreateSandbox handler + new `provision` helper)
- Modify: `internal/mcp/server.go` (Tools struct gains `daemonCtx context.Context`)
- Modify: `cmd/mcp.go` (pass the daemon ctx)
- Test: `internal/mcp/tools_test.go`

**Step 1: Write the failing test**

```go
func TestCreateSandboxReturnsImmediatelyWithProvisioning(t *testing.T) {
	tt, fb := newTestTools(t)
	// Stub the backend's Create to block — simulates a slow provision.
	createReturn := make(chan struct{})
	fb.createHook = func(o sandbox.CreateOpts) (*sandbox.Instance, error) {
		<-createReturn
		return &sandbox.Instance{Name: o.Name, Status: sandbox.StatusRunning, Addresses: []string{"10.0.0.1"}}, nil
	}

	tt.daemonCtx = context.Background()

	start := time.Now()
	out, err := tt.CreateSandbox(context.Background(), CreateSandboxIn{})
	if err != nil {
		t.Fatal(err)
	}
	if got := time.Since(start); got > 100*time.Millisecond {
		t.Errorf("CreateSandbox took %v; should return immediately", got)
	}
	if out.Status != "provisioning" {
		t.Errorf("Status = %q, want provisioning", out.Status)
	}

	// Unblock the backend; goroutine should now flip status to running.
	close(createReturn)
	deadline := time.After(2 * time.Second)
	for {
		got, _ := tt.State.Get(out.Name)
		if got.Status == "running" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("status never became running; final state: %+v", got)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
```

(`createHook` is a new field on `fakeSandbox` for capturing/customizing Create calls. Add it now if absent.)

**Step 2: Verify failure**

```
go test ./internal/mcp -run TestCreateSandboxReturnsImmediately -v
```

Expected: FAIL — current CreateSandbox is synchronous.

**Step 3: Implement**

In `internal/mcp/tools.go`:

Add `daemonCtx` to `Tools`:

```go
type Tools struct {
	// ... existing ...
	DaemonCtx context.Context // outlives any single request; provisioning goroutine inherits this
}
```

Refactor `CreateSandbox`:

```go
func (t *Tools) CreateSandbox(ctx context.Context, in CreateSandboxIn) (CreateSandboxOut, error) {
	image := in.Image
	if image == "" {
		image = t.DefaultImage
	}
	name := t.generateName()
	now := time.Now().UTC()

	t.State.Add(Sandbox{
		Name:           name,
		Label:          in.Label,
		Image:          image,
		Status:         "provisioning",
		CreatedAt:      now,
		LastActivityAt: now,
	})
	if err := t.persist(); err != nil {
		t.State.Remove(name)
		return CreateSandboxOut{}, fmt.Errorf("create %s: state save failed: %w", name, err)
	}

	go t.provision(name, in)

	return CreateSandboxOut{Name: name, Status: "provisioning"}, nil
}

func (t *Tools) provision(name string, in CreateSandboxIn) {
	m := t.Locks.For(name)
	m.Lock()
	defer m.Unlock()

	// Re-check: a destroy_sandbox call may have run between request return
	// and lock acquisition. If the sandbox is gone from state, exit cleanly.
	if _, ok := t.State.Get(name); !ok {
		t.log().Debug("provisioning aborted; sandbox already removed", "name", name)
		return
	}

	ctx := t.DaemonCtx
	if ctx == nil {
		ctx = context.Background()
	}

	image := in.Image
	if image == "" {
		image = t.DefaultImage
	}
	inst, err := t.Backend.Create(ctx, sandbox.CreateOpts{Name: name, Image: image})
	if err != nil {
		t.log().Error("create failed", "name", name, "err", err)
		t.State.MarkFailed(name, err)
		_ = t.persist()
		return
	}

	if err := t.Backend.Ready(ctx, name, 2*time.Minute); err != nil {
		t.log().Error("ready timed out", "name", name, "err", err)
		t.State.MarkFailed(name, err)
		_ = t.persist()
		return
	}

	if len(inst.Addresses) > 0 {
		t.State.SetIP(name, inst.Addresses[0])
	}
	t.State.MarkRunning(name)
	t.State.BumpActivity(name, time.Now().UTC())
	_ = t.persist()
	t.log().Info("provisioning complete", "name", name)
}
```

Add a `SetIP` helper to State (alongside SetStatus):

```go
func (s *State) SetIP(name, ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Sandboxes {
		if s.data.Sandboxes[i].Name == name {
			s.data.Sandboxes[i].IP = ip
			return
		}
	}
}
```

In `internal/mcp/server.go` `ServerOpts`, add `DaemonCtx context.Context` and forward to Tools.

In `cmd/mcp.go` `runMCP`, pass `ctx` (the daemon's root context) into `ServerOpts.DaemonCtx`.

**Step 4: Update Reaper to skip `provisioning` and `failed` sandboxes**

In `internal/mcp/reaper.go` `applyTTL`:

```go
func (r *Reaper) applyTTL(ctx context.Context, sb Sandbox, now time.Time) {
	// Don't reap during provisioning or after a failed build — failed sandboxes
	// stick around so the agent can see the error.
	if sb.Status == "provisioning" {
		return
	}
	if now.Sub(sb.CreatedAt) > r.HardDestroyAfter {
		// existing destroy logic
	}
	if sb.Status == "running" && now.Sub(sb.LastActivityAt) > r.IdleStopAfter {
		// existing stop logic
	}
}
```

(Failed sandboxes still hit hard-destroy after 24h — that's correct; we don't want them to leak forever.)

**Step 5: Run + commit**

```
go test ./internal/mcp -race -v
```

```bash
jj describe -m "feat(mcp): make create_sandbox async with provisioning goroutine"
jj new
```

---

## Task 3: Update `list_sandboxes` to surface the new status fields

**Files:**
- Modify: `internal/mcp/tools.go` (`SandboxView`, `ListSandboxes`)
- Test: `internal/mcp/tools_test.go`

**Step 1: Write the failing test**

```go
func TestListSandboxesIncludesErrorAndIP(t *testing.T) {
	tt, _ := newTestTools(t)
	tt.State.Add(Sandbox{
		Name:   "fail",
		Status: "failed",
		Error:  "boom",
	})
	tt.State.Add(Sandbox{
		Name:   "run",
		Status: "running",
		IP:     "10.0.0.5",
	})

	out, _ := tt.ListSandboxes(context.Background())
	var fail, run *SandboxView
	for i := range out.Sandboxes {
		if out.Sandboxes[i].Name == "fail" {
			fail = &out.Sandboxes[i]
		}
		if out.Sandboxes[i].Name == "run" {
			run = &out.Sandboxes[i]
		}
	}
	if fail == nil || fail.Error != "boom" {
		t.Errorf("expected error=boom on fail; got %+v", fail)
	}
	if run == nil || run.IP != "10.0.0.5" {
		t.Errorf("expected ip=10.0.0.5 on run; got %+v", run)
	}
}
```

**Step 2: Verify failure**

```
go test ./internal/mcp -run TestListSandboxesIncludesErrorAndIP -v
```

Expected: FAIL — fields don't exist yet.

**Step 3: Implement**

Update `SandboxView`:

```go
type SandboxView struct {
	Name           string    `json:"name"`
	Label          string    `json:"label,omitempty"`
	Status         string    `json:"status"`
	Error          string    `json:"error,omitempty"`
	IP             string    `json:"ip,omitempty"`
	Base           string    `json:"base,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	LastActivityAt time.Time `json:"last_activity_at"`
	IdleFor        string    `json:"idle_for"`
}
```

Update the loop in `ListSandboxes` to populate the new fields.

**Step 4: Run + commit**

```
go test ./internal/mcp -v
```

```bash
jj describe -m "feat(mcp): surface error/ip/base on list_sandboxes"
jj new
```

---

## Task 4: Add `[mcp.bases]` config section

**Files:**
- Modify: `internal/config/config.go` — add map of base configs.
- Modify: `internal/config/config_test.go`

**Step 1: Write the failing test**

```go
func TestMCPBasesParsed(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	cfgPath := filepath.Join(tmpDir, "pixels", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(`
[mcp.bases.python]
parent_image = "images:ubuntu/24.04"
setup_script = "~/scripts/python.sh"
description = "Python 3"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	b, ok := cfg.MCP.Bases["python"]
	if !ok {
		t.Fatalf("python base not parsed; got %+v", cfg.MCP.Bases)
	}
	if b.ParentImage != "images:ubuntu/24.04" {
		t.Errorf("ParentImage = %q", b.ParentImage)
	}
	if b.Description != "Python 3" {
		t.Errorf("Description = %q", b.Description)
	}
}
```

**Step 2: Verify failure**

```
go test ./internal/config -run TestMCPBases -v
```

Expected: FAIL — `cfg.MCP.Bases` undefined.

**Step 3: Implement**

In `internal/config/config.go`, add:

```go
type Base struct {
	ParentImage string `toml:"parent_image"`
	SetupScript string `toml:"setup_script"`
	Description string `toml:"description"`
}
```

Add `Bases map[string]Base` to the existing `MCP` struct:

```go
type MCP struct {
	// ... existing ...
	Bases map[string]Base `toml:"bases"`
}
```

`expandHome` the `SetupScript` paths after Load:

```go
// In Load() after env.Parse:
for name, b := range cfg.MCP.Bases {
	b.SetupScript = expandHome(b.SetupScript)
	cfg.MCP.Bases[name] = b
}
```

**Step 4: Run + commit**

```
go test ./internal/config -v
```

```bash
jj describe -m "feat(config): add [mcp.bases] config section"
jj new
```

---

## Task 5: Implement the `Builder` (in-process build dedup + failure cache)

**Files:**
- Create: `internal/mcp/builder.go`
- Create: `internal/mcp/builder_test.go`

**Step 1: Write the failing test**

```go
package mcp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBuilderDedupesConcurrentCalls(t *testing.T) {
	var doBuildCalls atomic.Int32
	b := &Builder{
		DoBuild: func(ctx context.Context, name string) error {
			doBuildCalls.Add(1)
			time.Sleep(50 * time.Millisecond)
			return nil
		},
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = b.Build(context.Background(), "alpha")
		}()
	}
	wg.Wait()

	if got := doBuildCalls.Load(); got != 1 {
		t.Errorf("DoBuild called %d times; want exactly 1 (deduplicated)", got)
	}
}

func TestBuilderCachesFailures(t *testing.T) {
	var doBuildCalls atomic.Int32
	b := &Builder{
		FailureTTL: 1 * time.Hour,
		DoBuild: func(ctx context.Context, name string) error {
			doBuildCalls.Add(1)
			return errors.New("build failed")
		},
	}

	if err := b.Build(context.Background(), "alpha"); err == nil {
		t.Fatal("first call should have errored")
	}
	if err := b.Build(context.Background(), "alpha"); err == nil {
		t.Fatal("cached call should have errored")
	}
	if got := doBuildCalls.Load(); got != 1 {
		t.Errorf("DoBuild called %d times; want exactly 1 (second hit cache)", got)
	}
}

func TestBuilderStatusReportsBuilding(t *testing.T) {
	started := make(chan struct{})
	finish := make(chan struct{})
	b := &Builder{
		DoBuild: func(ctx context.Context, name string) error {
			close(started)
			<-finish
			return nil
		},
	}

	go func() { _ = b.Build(context.Background(), "alpha") }()
	<-started
	if status, _ := b.Status("alpha"); status != "building" {
		t.Errorf("Status = %q, want building", status)
	}

	close(finish)
	deadline := time.After(2 * time.Second)
	for {
		if status, _ := b.Status("alpha"); status == "" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Status never cleared")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestBuilderStatusReportsFailed(t *testing.T) {
	b := &Builder{
		FailureTTL: 1 * time.Hour,
		DoBuild: func(ctx context.Context, name string) error {
			return errors.New("nope")
		},
	}
	_ = b.Build(context.Background(), "alpha")
	status, err := b.Status("alpha")
	if status != "failed" {
		t.Errorf("Status = %q, want failed", status)
	}
	if err == nil || err.Error() != "nope" {
		t.Errorf("err = %v, want nope", err)
	}
}
```

**Step 2: Verify failure**

```
go test ./internal/mcp -run TestBuilder -v
```

Expected: FAIL — Builder undefined.

**Step 3: Implement**

Create `internal/mcp/builder.go`:

```go
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
```

**Step 4: Run + commit**

```
go test ./internal/mcp -run TestBuilder -race -v
```

```bash
jj describe -m "feat(mcp): add Builder for in-process build dedup and failure caching"
jj new
```

---

## Task 6: Implement `BuildLock` (cross-process flock)

**Files:**
- Create: `internal/mcp/buildlock.go`
- Create: `internal/mcp/buildlock_test.go`

**Step 1: Write the failing test**

```go
package mcp

import (
	"path/filepath"
	"testing"
	"time"
)

func TestBuildLockSerialisesSameName(t *testing.T) {
	dir := t.TempDir()

	first, err := AcquireBuildLock(dir, "alpha")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// Second acquire should block until first is released.
	got := make(chan error, 1)
	go func() {
		_, err := AcquireBuildLock(dir, "alpha")
		got <- err
	}()

	select {
	case <-got:
		t.Fatal("second acquire returned before first was released")
	case <-time.After(50 * time.Millisecond):
		// expected: still blocked
	}

	first.Release()
	select {
	case err := <-got:
		if err != nil {
			t.Errorf("second acquire after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second acquire never returned after release")
	}
}

func TestBuildLockDifferentNamesIndependent(t *testing.T) {
	dir := t.TempDir()
	a, err := AcquireBuildLock(dir, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()

	b, err := AcquireBuildLock(dir, "beta")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Release()

	// If we got here without blocking, the locks are per-name — correct.
	_ = filepath.Join(dir, "irrelevant")
}
```

**Step 2: Verify failure**

```
go test ./internal/mcp -run TestBuildLock -v
```

Expected: FAIL — undefined.

**Step 3: Implement**

Create `internal/mcp/buildlock.go`:

```go
package mcp

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// BuildLock is a file-level exclusive lock for serialising base builds
// across the daemon and CLI processes.
type BuildLock struct {
	f *os.File
}

// AcquireBuildLock takes an exclusive flock on <dir>/builds/<name>.lock.
// Blocks if another process holds the lock for the same name.
func AcquireBuildLock(dir, name string) (*BuildLock, error) {
	lockDir := filepath.Join(dir, "builds")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		return nil, fmt.Errorf("create build lock dir: %w", err)
	}
	path := filepath.Join(lockDir, name+".lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open build lock %s: %w", path, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("flock %s: %w", path, err)
	}
	return &BuildLock{f: f}, nil
}

// Release drops the file lock. Safe to call multiple times.
func (l *BuildLock) Release() {
	if l.f == nil {
		return
	}
	_ = unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}
```

> **Engineer note:** if `golang.org/x/sys/unix` isn't directly imported anywhere yet, run `go mod tidy` after this change. It's already an indirect dependency.

**Step 4: Run + commit**

```
go test ./internal/mcp -run TestBuildLock -v
go mod tidy
```

```bash
jj describe -m "feat(mcp): add BuildLock (file flock) for cross-process build serialisation"
jj new
```

---

## Task 7: Implement the actual base-build operation

**Files:**
- Create: `internal/mcp/buildbase.go`
- Create: `internal/mcp/buildbase_test.go`

**Step 1: Define the build-base function**

The work: create a temp sandbox from `parent_image`, wait Ready, upload `setup_script`, run it, snapshot, delete the temp.

Create `internal/mcp/buildbase.go`:

```go
package mcp

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/deevus/pixels/internal/config"
	"github.com/deevus/pixels/sandbox"
)

// BuildBaseSnapshotName is the snapshot name format for base pixels.
func BuildBaseSnapshotName(name string) string { return "px-base-" + name }

// BuildBase executes one full base-pixel build:
//   1. Create temp sandbox from parent_image
//   2. Wait Ready
//   3. Upload setup_script via Files.WriteFile
//   4. Run `bash /tmp/setup.sh` via Backend.Run
//   5. CreateSnapshot named "px-base-<name>"
//   6. Delete the temp sandbox
//
// On any failure it cleans up the temp sandbox and returns the error.
// out receives setup-script stdout/stderr for streaming to the user.
func BuildBase(ctx context.Context, be sandbox.Sandbox, baseCfg config.Base, name string, out io.Writer) error {
	if baseCfg.ParentImage == "" {
		return fmt.Errorf("base %q: parent_image not set", name)
	}
	if baseCfg.SetupScript == "" {
		return fmt.Errorf("base %q: setup_script not set", name)
	}

	scriptBytes, err := os.ReadFile(baseCfg.SetupScript)
	if err != nil {
		return fmt.Errorf("read setup_script %s: %w", baseCfg.SetupScript, err)
	}

	tempName := fmt.Sprintf("px-build-%s-%d", name, time.Now().Unix())

	fmt.Fprintf(out, "==> Creating temp sandbox %s from %s\n", tempName, baseCfg.ParentImage)
	if _, err := be.Create(ctx, sandbox.CreateOpts{Name: tempName, Image: baseCfg.ParentImage}); err != nil {
		return fmt.Errorf("create temp: %w", err)
	}

	cleanup := func() {
		fmt.Fprintf(out, "==> Cleaning up temp sandbox %s\n", tempName)
		if err := be.Delete(context.Background(), tempName); err != nil {
			fmt.Fprintf(out, "WARN: temp cleanup failed: %v\n", err)
		}
	}

	fmt.Fprintf(out, "==> Waiting for sandbox to be ready\n")
	if err := be.Ready(ctx, tempName, 5*time.Minute); err != nil {
		cleanup()
		return fmt.Errorf("ready: %w", err)
	}

	fmt.Fprintf(out, "==> Uploading setup script (%d bytes)\n", len(scriptBytes))
	if err := be.WriteFile(ctx, tempName, "/tmp/pixels-setup.sh", scriptBytes, 0o755); err != nil {
		cleanup()
		return fmt.Errorf("upload script: %w", err)
	}

	fmt.Fprintf(out, "==> Running setup script\n")
	exit, err := be.Run(ctx, tempName, sandbox.ExecOpts{
		Cmd:    []string{"bash", "/tmp/pixels-setup.sh"},
		Stdout: out,
		Stderr: out,
		Root:   true,
	})
	if err != nil {
		cleanup()
		return fmt.Errorf("setup script: %w", err)
	}
	if exit != 0 {
		cleanup()
		return fmt.Errorf("setup script exited %d", exit)
	}

	fmt.Fprintf(out, "==> Snapshotting as %s\n", BuildBaseSnapshotName(name))
	if err := be.CreateSnapshot(ctx, tempName, BuildBaseSnapshotName(name)); err != nil {
		cleanup()
		return fmt.Errorf("snapshot: %w", err)
	}

	cleanup()
	fmt.Fprintf(out, "==> Done\n")
	_ = filepath.Base("") // silence unused-import if filepath isn't used elsewhere
	return nil
}
```

**Step 2: Write a basic test**

```go
package mcp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/deevus/pixels/internal/config"
)

func TestBuildBaseSequenceOfBackendCalls(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "setup.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	be := newFakeSandbox()
	var buf bytes.Buffer
	err := BuildBase(context.Background(), be, config.Base{
		ParentImage: "images:ubuntu/24.04",
		SetupScript: scriptPath,
	}, "python", &buf)
	if err != nil {
		t.Fatalf("BuildBase: %v", err)
	}

	if len(be.created) != 1 {
		t.Errorf("expected exactly one temp sandbox created, got %d", len(be.created))
	}
	if got := be.snapshots["px-base-python"]; got == "" {
		t.Errorf("expected snapshot px-base-python; got snapshots=%v", be.snapshots)
	}
	if len(be.deleted) != 1 {
		t.Errorf("expected temp deleted; got %v", be.deleted)
	}
}
```

(Add `snapshots map[string]string` to `fakeSandbox` plus a stub `CreateSnapshot` that records into it. Adjust existing `Get` impl if needed to return the temp sandbox.)

**Step 3: Run + commit**

```
go test ./internal/mcp -run TestBuildBase -v
```

```bash
jj describe -m "feat(mcp): add BuildBase function (create temp, run setup, snapshot, cleanup)"
jj new
```

---

## Task 8: Wire `Builder` + `BuildLock` + `BuildBase` into `Tools.CreateSandbox`

**Files:**
- Modify: `internal/mcp/tools.go` — `CreateSandbox` adds `base?` arg, `provision` calls into Builder.
- Modify: `internal/mcp/server.go` — Tools holds `Builder`, `Cfg *config.Config` (for base lookup), `BuildLockDir string`.
- Modify: `cmd/mcp.go` — construct Builder + plumb config + lock dir.
- Test: `internal/mcp/tools_test.go`

**Step 1: Write the failing test**

```go
func TestCreateSandboxWithBaseClonesFromSnapshot(t *testing.T) {
	tt, fb := newTestTools(t)
	tt.daemonCtx = context.Background()
	tt.Cfg = &config.Config{
		MCP: config.MCP{
			Bases: map[string]config.Base{
				"python": {
					ParentImage: "images:ubuntu/24.04",
					SetupScript: writeTempScript(t),
				},
			},
		},
	}
	tt.BuildLockDir = t.TempDir()
	tt.Builder = &Builder{}
	tt.Builder.DoBuild = func(ctx context.Context, name string) error {
		return BuildBase(ctx, tt.Backend, tt.Cfg.MCP.Bases[name], name, io.Discard)
	}
	// Pretend the snapshot already exists for this test.
	fb.snapshots["px-base-python"] = "ready"

	out, err := tt.CreateSandbox(context.Background(), CreateSandboxIn{Base: "python"})
	if err != nil {
		t.Fatal(err)
	}
	// wait for provisioning to flip to running
	mustEventually(t, func() bool {
		got, _ := tt.State.Get(out.Name)
		return got.Status == "running"
	})
	// verify CloneFrom was used, not Create
	if len(fb.cloned) != 1 || fb.cloned[0].source != "px-base-python" {
		t.Errorf("expected CloneFrom px-base-python; got %+v", fb.cloned)
	}
}
```

(Helpers `writeTempScript`, `mustEventually`, and the `cloned` tracking on `fakeSandbox` are new additions.)

**Step 2: Verify failure**

```
go test ./internal/mcp -run TestCreateSandboxWithBaseClonesFromSnapshot -v
```

Expected: FAIL — `Base` field, Builder, etc. unwired.

**Step 3: Implement**

Add `Base` to `CreateSandboxIn`:

```go
type CreateSandboxIn struct {
	Label string `json:"label,omitempty"`
	Image string `json:"image,omitempty"`
	Base  string `json:"base,omitempty"`
}
```

Add fields to `Tools`:

```go
type Tools struct {
	// ... existing ...
	Cfg          *config.Config
	Builder      *Builder
	BuildLockDir string
}
```

Refactor `provision`:

```go
func (t *Tools) provision(name string, in CreateSandboxIn) {
	m := t.Locks.For(name)
	m.Lock()
	defer m.Unlock()

	if _, ok := t.State.Get(name); !ok {
		return
	}

	ctx := t.DaemonCtx
	if ctx == nil {
		ctx = context.Background()
	}

	if in.Base != "" {
		t.provisionFromBase(ctx, name, in)
		return
	}
	t.provisionFromImage(ctx, name, in)
}

func (t *Tools) provisionFromBase(ctx context.Context, name string, in CreateSandboxIn) {
	baseCfg, ok := t.Cfg.MCP.Bases[in.Base]
	if !ok {
		t.State.MarkFailed(name, fmt.Errorf("base %q not declared in config", in.Base))
		_ = t.persist()
		return
	}

	// Ensure the snapshot exists (build if not). This blocks for the duration
	// of the build, which may be minutes — that's fine; we're in a goroutine.
	if !t.snapshotExists(ctx, BuildBaseSnapshotName(in.Base)) {
		if err := t.Builder.Build(ctx, in.Base); err != nil {
			t.State.MarkFailed(name, fmt.Errorf("build base %s: %w", in.Base, err))
			_ = t.persist()
			return
		}
	}

	cloneName := name
	if err := t.Backend.CloneFrom(ctx, BuildBaseSnapshotName(in.Base), in.Label, cloneName); err != nil {
		t.State.MarkFailed(name, fmt.Errorf("clone: %w", err))
		_ = t.persist()
		return
	}
	if err := t.Backend.Ready(ctx, cloneName, 2*time.Minute); err != nil {
		t.State.MarkFailed(name, fmt.Errorf("ready: %w", err))
		_ = t.persist()
		return
	}

	if inst, err := t.Backend.Get(ctx, cloneName); err == nil && len(inst.Addresses) > 0 {
		t.State.SetIP(cloneName, inst.Addresses[0])
	}
	t.State.MarkRunning(name)
	t.State.BumpActivity(name, time.Now().UTC())
	_ = t.persist()
	_ = baseCfg
}

func (t *Tools) snapshotExists(ctx context.Context, snapName string) bool {
	// We don't know which container owns the snapshot at this layer. The
	// backend's ListSnapshots takes a sandbox name; bases live as standalone
	// snapshots. Use a backend-specific helper if available, otherwise rely
	// on Backend.CloneFrom returning a NotFound error and treat that as
	// "needs to be built". Simplest approach for v1: try CloneFrom into a
	// throwaway name and inspect the error.
	//
	// (Adapt this to whatever the backend's snapshot-existence API is —
	// `incusclient.HasInstanceSnapshot` etc.)
	return false  // pessimistic: always build first time. Builder dedupes correctly.
}
```

> **Engineer note:** the `snapshotExists` logic is the trickiest backend-specific detail. The cleanest solution is a new method on `Backend` (e.g. `Backend.SnapshotExists(name string) bool`). Adding it requires touching both Incus and TrueNAS backends. For v1, the pessimistic "always call Builder.Build" path works because:
> - Builder is idempotent: if the snapshot already exists, BuildBase will fail at `CreateSnapshot` (snapshot name conflict). We can detect that and treat as success.
> - Or BuildBase can `ListSnapshots` first via the temp sandbox… but that's circular.
>
> The right answer is the new `Backend.SnapshotExists(name string) (bool, error)` method. Add it as part of this task. In Incus: `incusclient.GetInstanceSnapshot` returns `404` for missing — wrap. In TrueNAS: similar. This adds one method to the interface; both backends implement; existing CLI sandboxes are unaffected. Estimated 30 LOC.

In `cmd/mcp.go`:

```go
locks := &mcppkg.SandboxLocks{}
buildLockDir := filepath.Join(filepath.Dir(stateFile), "")  // or a dedicated path
builder := &mcppkg.Builder{
	FailureTTL: 10 * time.Minute,
}
builder.DoBuild = func(ctx context.Context, name string) error {
	bl, err := mcppkg.AcquireBuildLock(buildLockDir, name)
	if err != nil {
		return err
	}
	defer bl.Release()
	baseCfg, ok := cfg.MCP.Bases[name]
	if !ok {
		return fmt.Errorf("base %q not declared", name)
	}
	return mcppkg.BuildBase(ctx, sb, baseCfg, name, log.Writer()) // or a buffer
}

mux, _ := mcppkg.NewServer(mcppkg.ServerOpts{
	// ... existing ...
	Cfg:          cfg,
	Builder:      builder,
	BuildLockDir: buildLockDir,
})
```

(`log.Writer()` — slog.Logger doesn't expose an io.Writer; use a wrapper or capture build output to a fresh buffer per call. Simplest: pass `os.Stderr` for now and rely on `--verbose` semantics from Plan A.)

**Step 4: Run + commit**

```
go test ./internal/mcp -race -v
go build ./...
```

```bash
jj describe -m "feat(mcp): wire base arg through CreateSandbox via Builder + BuildLock"
jj new
```

---

## Task 9: Add `list_bases` MCP tool

**Files:**
- Modify: `internal/mcp/tools.go` — new tool handler.
- Modify: `internal/mcp/server.go` — register tool.
- Test: `internal/mcp/tools_test.go`

**Step 1: Write the failing test**

```go
func TestListBasesReturnsConfiguredBases(t *testing.T) {
	tt, fb := newTestTools(t)
	tt.Cfg = &config.Config{
		MCP: config.MCP{
			Bases: map[string]config.Base{
				"python": {ParentImage: "images:ubuntu/24.04", Description: "Python 3"},
				"node":   {ParentImage: "images:ubuntu/24.04", Description: "Node 22"},
			},
		},
	}
	tt.Builder = &Builder{}
	fb.snapshots["px-base-python"] = "ready"  // python is built; node is not

	out, err := tt.ListBases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, b := range out.Bases {
		got[b.Name] = b.Status
	}
	if got["python"] != "ready" {
		t.Errorf("python status = %q, want ready", got["python"])
	}
	if got["node"] != "missing" {
		t.Errorf("node status = %q, want missing", got["node"])
	}
}
```

**Step 2: Verify failure**

```
go test ./internal/mcp -run TestListBasesReturnsConfiguredBases -v
```

Expected: FAIL — undefined.

**Step 3: Implement**

Add types and handler to `internal/mcp/tools.go`:

```go
type ListBasesOut struct {
	Bases []BaseView `json:"bases"`
}

type BaseView struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	ParentImage string `json:"parent_image"`
	Status      string `json:"status"` // "ready" | "missing" | "building" | "failed"
	Error       string `json:"error,omitempty"`
}

func (t *Tools) ListBases(ctx context.Context) (ListBasesOut, error) {
	if t.Cfg == nil {
		return ListBasesOut{}, nil
	}
	out := make([]BaseView, 0, len(t.Cfg.MCP.Bases))
	for name, b := range t.Cfg.MCP.Bases {
		view := BaseView{
			Name:        name,
			Description: b.Description,
			ParentImage: b.ParentImage,
		}

		// In-flight or recently failed?
		if t.Builder != nil {
			if status, err := t.Builder.Status(name); status != "" {
				view.Status = status
				if err != nil {
					view.Error = err.Error()
				}
				out = append(out, view)
				continue
			}
		}

		// Snapshot present or absent?
		if t.snapshotExists(ctx, BuildBaseSnapshotName(name)) {
			view.Status = "ready"
		} else {
			view.Status = "missing"
		}
		out = append(out, view)
	}
	return ListBasesOut{Bases: out}, nil
}
```

Register the tool in `internal/mcp/server.go`:

```go
sdk.AddTool(srv, &sdk.Tool{Name: "list_bases", Description: "List declared base pixels and their status (ready, missing, building, failed)."}, tools.ListBases)
```

Update `create_sandbox` description to mention the `base` field:

```go
sdk.AddTool(srv, &sdk.Tool{Name: "create_sandbox", Description: "Create an ephemeral sandbox container. Pass `base` to clone from a pre-built base pixel (faster); pass `image` for raw Incus alias (slower, deprecated)."}, tools.CreateSandbox)
```

**Step 4: Run + commit**

```
go test ./internal/mcp -v
```

```bash
jj describe -m "feat(mcp): add list_bases tool and base arg on create_sandbox"
jj new
```

---

## Task 10: CLI subcommands `pixels mcp build-base` / `rebuild-base` / `delete-base` / `list-bases`

**Files:**
- Create: `cmd/mcp_base.go`

**Step 1: Implement the subcommands**

The CLI calls `mcppkg.BuildBase` directly with `os.Stderr` as the output writer. Cross-process serialisation against the running daemon (if any) is via `BuildLock`.

```go
package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/deevus/pixels/internal/config"
	mcppkg "github.com/deevus/pixels/internal/mcp"
	"github.com/spf13/cobra"
)

func init() {
	mcpCmd.AddCommand(mcpBuildBaseCmd)
	mcpCmd.AddCommand(mcpRebuildBaseCmd)
	mcpCmd.AddCommand(mcpDeleteBaseCmd)
	mcpCmd.AddCommand(mcpListBasesCmd)
}

var mcpBuildBaseCmd = &cobra.Command{
	Use:   "build-base <name>",
	Short: "Build a base pixel from config",
	Args:  cobra.ExactArgs(1),
	RunE:  runBuildBase,
}

var mcpRebuildBaseCmd = &cobra.Command{
	Use:   "rebuild-base <name>",
	Short: "Delete an existing base pixel snapshot and rebuild",
	Args:  cobra.ExactArgs(1),
	RunE:  runRebuildBase,
}

var mcpDeleteBaseCmd = &cobra.Command{
	Use:   "delete-base <name>",
	Short: "Delete a base pixel snapshot",
	Args:  cobra.ExactArgs(1),
	RunE:  runDeleteBase,
}

var mcpListBasesCmd = &cobra.Command{
	Use:   "list-bases",
	Short: "List declared base pixels and their status",
	RunE:  runListBases,
}

func runBuildBase(cmd *cobra.Command, args []string) error {
	name := args[0]
	cfg, sb, lockDir, err := setupBaseCmd(cmd)
	if err != nil {
		return err
	}
	defer sb.Close()

	baseCfg, ok := cfg.MCP.Bases[name]
	if !ok {
		return fmt.Errorf("base %q not declared in config", name)
	}

	bl, err := mcppkg.AcquireBuildLock(lockDir, name)
	if err != nil {
		return err
	}
	defer bl.Release()

	return mcppkg.BuildBase(context.Background(), sb, baseCfg, name, os.Stderr)
}

func runRebuildBase(cmd *cobra.Command, args []string) error {
	name := args[0]
	cfg, sb, lockDir, err := setupBaseCmd(cmd)
	if err != nil {
		return err
	}
	defer sb.Close()

	baseCfg, ok := cfg.MCP.Bases[name]
	if !ok {
		return fmt.Errorf("base %q not declared in config", name)
	}

	bl, err := mcppkg.AcquireBuildLock(lockDir, name)
	if err != nil {
		return err
	}
	defer bl.Release()

	// Best-effort: delete existing snapshot if present.
	// Backend.DeleteSnapshot takes (sandboxName, label). For standalone
	// base snapshots, we may need a backend helper that handles this.
	// Skip the delete for v1 and document that rebuild-base requires
	// running delete-base first if the snapshot exists.
	fmt.Fprintf(os.Stderr, "WARN: existing snapshot not auto-deleted; run `pixels mcp delete-base %s` first if it exists\n", name)
	return mcppkg.BuildBase(context.Background(), sb, baseCfg, name, os.Stderr)
}

func runDeleteBase(cmd *cobra.Command, args []string) error {
	name := args[0]
	_, sb, _, err := setupBaseCmd(cmd)
	if err != nil {
		return err
	}
	defer sb.Close()

	// As above — base snapshots are standalone; the backend may need a new
	// method for "delete this top-level snapshot". For Incus, the snapshot
	// belongs to whatever container created it; we'd need to track that.
	// For v1 this is a stub; document the limitation.
	fmt.Fprintf(os.Stderr, "delete-base for %s: not implemented in v1; manually use `incus delete <snapshot>`\n", name)
	return nil
}

func runListBases(cmd *cobra.Command, args []string) error {
	cfg, _, _, err := setupBaseCmd(cmd)
	if err != nil {
		return err
	}
	w := newTabWriter(cmd)
	defer w.Flush()
	fmt.Fprintln(w, "NAME\tPARENT\tDESCRIPTION")
	for name, b := range cfg.MCP.Bases {
		fmt.Fprintf(w, "%s\t%s\t%s\n", name, b.ParentImage, b.Description)
	}
	return nil
}

// setupBaseCmd loads config, opens a sandbox backend, and returns the lock
// directory. Common to all base subcommands.
func setupBaseCmd(cmd *cobra.Command) (*config.Config, sandbox.Sandbox, string, error) {
	cfg, ok := cmd.Context().Value(configKey).(*config.Config)
	if !ok {
		return nil, nil, "", fmt.Errorf("config not loaded")
	}
	sb, err := openSandbox()
	if err != nil {
		return nil, nil, "", err
	}
	stateFile := cfg.MCPStateFile()
	lockDir := filepath.Dir(stateFile)
	return cfg, sb, lockDir, nil
}
```

> **Engineer note:** the `delete-base` and `rebuild-base` standalone-snapshot semantics need a backend helper that v1 can stub. Document the v1 limitation; add proper support in a follow-up.

**Step 2: Build and confirm**

```
go build -o pixels .
./pixels mcp build-base --help
./pixels mcp list-bases
```

Expected: help text and (for `list-bases`) tabular output of declared bases.

**Step 3: Commit**

```bash
jj describe -m "feat(cmd): add 'pixels mcp build-base/rebuild-base/delete-base/list-bases' subcommands"
jj new
```

---

## Task 11: README updates and tool description refresh

**Files:**
- Modify: `README.md`

**Step 1: Add new sections**

Append after the existing "Tools" section in the MCP server docs:

```markdown
### Base pixels

Bases are pre-built ZFS snapshots that `create_sandbox(base="X")` clones
from. Cloning is sub-5s; building from scratch takes minutes.

Configure in `~/.config/pixels/config.toml`:

    [mcp.bases.python]
    parent_image = "images:ubuntu/24.04"
    setup_script = "~/.config/pixels/bases/python.sh"
    description = "Ubuntu 24.04 + python3, pip, pipx"

The `setup_script` is a regular shell script. Pre-warm with:

    pixels mcp build-base python

Or just call `create_sandbox(base="python")` from an agent — the daemon
will build the base in the background on first use. The `list_bases`
MCP tool shows status (`ready` / `missing` / `building` / `failed`).

### Provisioning is async

`create_sandbox` returns immediately with `status: "provisioning"`.
The agent should poll `list_sandboxes` until status flips to `running`
or `failed`. A failed sandbox includes an `error` field describing
what went wrong.

For simple use without a base, provisioning takes ~30s. With a built
base, ~5s. With an unbuilt base, several minutes (the build runs
behind the scenes).
```

**Step 2: Commit**

```bash
jj describe -m "docs: document base pixels and async provisioning"
jj new
```

---

## Task 12: Final cleanup

**Step 1: Run the full matrix**

```
go test ./... -race
go build ./...
go vet ./...
```

All clean.

**Step 2: Smoke test against a live backend**

In one terminal:

```
pixels mcp --verbose
```

In another, exercise each new path:

```
# Define a base in config first
echo '[mcp.bases.tiny]
parent_image = "images:alpine/edge"
setup_script = "/tmp/tiny.sh"
description = "Alpine"' >> ~/.config/pixels/config.toml
echo '#!/bin/sh
apk add bash' > /tmp/tiny.sh

# Pre-warm
pixels mcp build-base tiny

# Verify
pixels mcp list-bases

# Now via MCP: create_sandbox(base="tiny") should clone in <5s
# (use a real MCP client or curl with handcrafted JSON-RPC)
```

**Step 3: Final commit**

```bash
jj describe -m "chore(mcp): final async + bases cleanup"
jj new
```

---

## Out of scope for this plan

- `delete-base` / `rebuild-base` proper standalone-snapshot semantics — v1 stubs them; track as follow-up.
- Drift detection on setup_script changes — explicit YAGNI per design discussion.
- Server-Sent push notifications for status changes (current path is poll via `list_sandboxes`).
- Image-arg deprecation — kept for back-compat through one release; remove after agents migrate to `base`.
- `Backend.SnapshotExists` proper implementation — Task 8 has an engineer note.

If any of these turn out to bite real workflows, raise as follow-up tasks; do not extend this plan in flight.
