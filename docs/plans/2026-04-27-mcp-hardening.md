# MCP Hardening Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Land the must-fix items from the code review plus the bug-fix backlog from user testing. Establish a structured logger (`slog`) and the shared `SandboxLocks` primitive that Plan B (async + bases) will build on.

**Architecture:** Mostly mechanical fixes; one foundational refactor (extract `SandboxLocks` from `Tools` so the reaper can share it) and one new package-wide capability (`slog` logger with a `--verbose` flag). All other tasks ride on those two.

**Tech Stack:**
- Go 1.25 (`log/slog` from stdlib)
- existing: `internal/mcp/`, `internal/config/`, `sandbox/`, `cmd/mcp.go`
- no new dependencies

**Worktree:** This plan executes in the existing worktree at `/Users/sh/Projects/pixels/.worktrees/mcp-sandbox` on branch `feat/mcp-sandbox-server`. Existing implementation HEAD is `b3d36eb`.

**VCS:** This project uses Jujutsu (jj) in colocated mode. **Do not run `git commit`.** Each task ends with:

```bash
jj describe -m "type: subject"
jj new
```

`jj describe` sets the description on the current change; `jj new` starts a new empty change. The pair is jj-native equivalent of `git commit`. If a step says "commit", run those two commands.

---

## Engineer-orientation notes

Before starting:

1. **Read the design doc** at `docs/plans/2026-04-27-mcp-sandbox-server-design.md` and the original implementation plan at `docs/plans/2026-04-27-mcp-sandbox-server.md`. Skim the code-review feedback summarised in this plan's Tasks 1–6.
2. **Read** `internal/mcp/tools.go`, `internal/mcp/reaper.go`, `internal/mcp/state.go`, `sandbox/filesexec.go`. Those are the four files touched most.
3. **Read** `/tmp/pixels-mcp/BUGS.md` (the user-testing bug list) for context on Tasks 7–11.
4. **Run** `go test ./... -race` once before starting to confirm the baseline is clean.

**Lock-acquisition order** (this plan introduces `SandboxLocks` formally; Plan B documents the full graph). For now: `SandboxLocks.For(name)` is held longest, `State.mu` shortest. Never call backend I/O while holding `State.mu`.

**Test conventions:** table-driven where it fits; existing pattern in `internal/mcp/tools_test.go` and `cmd/resolve_test.go`.

**Commit cadence:** one commit (= one `jj describe` + `jj new`) per task. Each task is small enough that a single-line description is enough.

---

## Task 1: Introduce `slog` logger and `--verbose` flag

**Why first:** Tasks 2 (lock coordination) and 3 (Save error handling) both want a logger to call into. Get the plumbing in place once.

**Files:**
- Create: `internal/mcp/log.go`
- Modify: `internal/mcp/server.go` (accept logger via ServerOpts)
- Modify: `internal/mcp/tools.go` (Tools struct holds *slog.Logger)
- Modify: `internal/mcp/reaper.go` (Reaper struct holds *slog.Logger)
- Modify: `internal/mcp/state.go` (State holds optional *slog.Logger; existing direct `os.Stderr` writes route through it)
- Modify: `cmd/mcp.go` (`--verbose` flag, build the logger, pass to NewServer + Reaper)
- Test: `internal/mcp/log_test.go`

**Step 1: Write the failing test**

Create `internal/mcp/log_test.go`:

```go
package mcp

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestNewLoggerVerboseEmitsDebug(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, true)
	log.Debug("hello")
	if !strings.Contains(buf.String(), "hello") {
		t.Errorf("verbose logger should emit Debug; got %q", buf.String())
	}
}

func TestNewLoggerNonVerboseDropsDebug(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, false)
	log.Debug("hello")
	if buf.Len() != 0 {
		t.Errorf("non-verbose logger should drop Debug; got %q", buf.String())
	}
}

func TestNewLoggerAlwaysEmitsErrorLevel(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, false)
	log.Error("oops")
	if !strings.Contains(buf.String(), "oops") {
		t.Errorf("non-verbose logger should still emit Error; got %q", buf.String())
	}
}

func TestNopLoggerDoesNotPanic(t *testing.T) {
	log := NopLogger()
	log.Info("anything")
	log.Error("anything")
	// no assertion — just must not panic
}
```

**Step 2: Run to verify failure**

```
go test ./internal/mcp -run TestNewLogger -v
go test ./internal/mcp -run TestNopLogger -v
```

Expected: FAIL — `NewLogger` / `NopLogger` undefined.

**Step 3: Implement `internal/mcp/log.go`**

```go
package mcp

import (
	"io"
	"log/slog"
)

// NewLogger returns a slog.Logger that writes to w. When verbose is true
// the level is Debug; otherwise Info. Format is text (one line per record)
// — structured but human-readable for daemon stderr.
func NewLogger(w io.Writer, verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	h := slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(h)
}

// NopLogger returns a logger that discards everything. Use in tests where
// log output isn't being asserted on.
func NopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
```

**Step 4: Run tests**

```
go test ./internal/mcp -run "TestNewLogger|TestNopLogger" -v
```

Expected: PASS.

**Step 5: Plumb the logger through `Tools`, `Reaper`, `State`, and `NewServer`**

In `internal/mcp/tools.go`, add to `Tools` struct (near the existing fields):

```go
Log *slog.Logger // not nil; defaults to NopLogger if not set
```

In `internal/mcp/reaper.go`, add to `Reaper` struct:

```go
Log *slog.Logger
```

In `internal/mcp/state.go`, add optional logger field:

```go
type State struct {
	path string
	mu   sync.RWMutex
	data stateData
	log  *slog.Logger // optional; nil-safe via stateLog()
}

func (s *State) stateLog() *slog.Logger {
	if s.log == nil {
		return NopLogger()
	}
	return s.log
}

// SetLogger assigns the logger after construction. Call once at startup.
func (s *State) SetLogger(l *slog.Logger) { s.log = l }
```

Replace the existing `fmt.Fprintf(os.Stderr, "pixels mcp: state file corrupt, ...")` call in `LoadState` with an explicit one-time warn (it runs before any logger could be set). Leave that one as-is for now — it's the only `os.Stderr` write that fires before logger construction.

In `internal/mcp/server.go`, add to `ServerOpts`:

```go
Log *slog.Logger
```

In `NewServer`, default to `NopLogger()` if `opts.Log` is nil, then propagate to `Tools`:

```go
log := opts.Log
if log == nil {
	log = NopLogger()
}
tools := &Tools{
	State:          opts.State,
	Backend:        opts.Backend,
	// ... existing fields ...
	Log:            log,
}
```

**Step 6: Add `--verbose` flag in `cmd/mcp.go`**

Add the package-level var:

```go
var mcpVerbose bool
```

In `init()`:

```go
mcpCmd.Flags().BoolVarP(&mcpVerbose, "verbose", "v", false, "log at debug level (tool entry/exit, backend calls)")
```

In `runMCP`, build the logger once and pass it everywhere:

```go
log := mcppkg.NewLogger(os.Stderr, mcpVerbose)
state.SetLogger(log)

mux, _ := mcppkg.NewServer(mcppkg.ServerOpts{
	State:          state,
	Backend:        sb,
	// ... existing ...
	Log:            log,
}, cfg.MCP.EndpointPath)

reaper := &mcppkg.Reaper{
	State:            state,
	Backend:          sb,
	IdleStopAfter:    idle,
	HardDestroyAfter: hard,
	Log:              log,
}
```

**Step 7: Run all tests**

```
go test ./... -race
go build ./...
```

Expected: PASS, build clean. Existing tests still pass because Tools and Reaper fields are zero-valued — anywhere they call `t.Log` we'll add a nil-guard pattern, OR we initialise them with `NopLogger()` in `NewServer`. Use the latter (simpler).

In `Tools` and `Reaper`, anywhere you would call the logger, write a small helper:

```go
func (t *Tools) log() *slog.Logger {
	if t.Log == nil {
		return NopLogger()
	}
	return t.Log
}
```

Use `t.log().Debug(...)` etc. Same pattern for `Reaper.log()`.

**Step 8: Commit**

```bash
jj describe -m "feat(mcp): add slog logger and --verbose flag"
jj new
```

---

## Task 2: Extract `SandboxLocks` shared between Tools and Reaper (Critical #1 from review)

**Files:**
- Create: `internal/mcp/locks.go`
- Create: `internal/mcp/locks_test.go`
- Modify: `internal/mcp/tools.go` (replace inline `sbLocks` with `*SandboxLocks`)
- Modify: `internal/mcp/reaper.go` (accept `*SandboxLocks`, use `TryLock`)
- Modify: `internal/mcp/server.go` (construct one `*SandboxLocks`, pass to Tools)
- Modify: `cmd/mcp.go` (pass the same `*SandboxLocks` to Reaper)

**Step 1: Write the failing test**

Create `internal/mcp/locks_test.go`:

```go
package mcp

import (
	"sync"
	"testing"
)

func TestSandboxLocksReturnsSameMutexForName(t *testing.T) {
	l := &SandboxLocks{}
	a := l.For("alpha")
	b := l.For("alpha")
	if a != b {
		t.Errorf("expected same *Mutex pointer for the same name; got %p and %p", a, b)
	}
}

func TestSandboxLocksReturnsDistinctMutexForDifferentNames(t *testing.T) {
	l := &SandboxLocks{}
	a := l.For("alpha")
	b := l.For("beta")
	if a == b {
		t.Errorf("expected distinct mutexes for different names")
	}
}

func TestSandboxLocksConcurrentAccessNoRace(t *testing.T) {
	l := &SandboxLocks{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m := l.For("sb")
			m.Lock()
			m.Unlock()
		}(i)
	}
	wg.Wait()
}
```

**Step 2: Verify failure**

```
go test ./internal/mcp -run TestSandboxLocks -v
```

Expected: FAIL — type undefined.

**Step 3: Implement**

Create `internal/mcp/locks.go`:

```go
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
```

**Step 4: Run tests**

```
go test ./internal/mcp -run TestSandboxLocks -race -v
```

Expected: PASS.

**Step 5: Replace `Tools.sbLocks` with `*SandboxLocks`**

In `internal/mcp/tools.go`:

- Remove the existing fields `mu sync.Mutex` and `sbLocks map[string]*sync.Mutex` from `Tools`. (If the existing implementation calls them by different names, find them via `grep`.)
- Add a single field `Locks *SandboxLocks`.
- Remove the `sbMutex(name)` method.
- Replace every `t.sbMutex(name)` call site with `t.Locks.For(name)`.

In `internal/mcp/server.go` `NewServer`, construct a `*SandboxLocks` once (or accept it via `ServerOpts`) and pass it to `Tools`:

```go
// In ServerOpts, add:
Locks *SandboxLocks  // shared with Reaper; constructed by caller

// In NewServer:
if opts.Locks == nil {
	opts.Locks = &SandboxLocks{}
}
tools := &Tools{
	// ... existing ...
	Locks: opts.Locks,
}
```

**Step 6: Update Reaper to use `*SandboxLocks` with TryLock**

In `internal/mcp/reaper.go`, add to `Reaper`:

```go
Locks *SandboxLocks
```

Refactor `Tick` to skip locked sandboxes. Extract a helper to keep the loop body small:

```go
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
	if r.Locks == nil {
		// fall back to old (unsafe) behaviour for tests that don't set Locks
		r.applyTTL(ctx, sb, now)
		return
	}
	m := r.Locks.For(sb.Name)
	if !m.TryLock() {
		r.log().Debug("reaper skipped busy sandbox", "name", sb.Name)
		return
	}
	defer m.Unlock()
	r.applyTTL(ctx, sb, now)
}

func (r *Reaper) applyTTL(ctx context.Context, sb Sandbox, now time.Time) {
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
```

`r.now()` and `r.log()` are already-existing or about-to-add helpers (see Task 1).

**Step 7: Wire in `cmd/mcp.go`**

```go
locks := &mcppkg.SandboxLocks{}

mux, _ := mcppkg.NewServer(mcppkg.ServerOpts{
	// ... existing ...
	Locks: locks,
	Log:   log,
}, cfg.MCP.EndpointPath)

reaper := &mcppkg.Reaper{
	State:            state,
	Backend:          sb,
	Locks:            locks,
	IdleStopAfter:    idle,
	HardDestroyAfter: hard,
	Log:              log,
}
```

**Step 8: Add a regression test for reaper-skips-busy**

Append to `internal/mcp/reaper_test.go`:

```go
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
```

**Step 9: Run all tests**

```
go test ./... -race
```

Expected: PASS, no race detector warnings.

**Step 10: Commit**

```bash
jj describe -m "feat(mcp): extract SandboxLocks; reaper TryLocks before Stop/Delete"
jj new
```

---

## Task 3: Surface `State.Save` errors instead of swallowing (Important #2 from review)

**Files:**
- Modify: `internal/mcp/tools.go` (~10 sites of `_ = t.State.Save()`)
- Modify: `internal/mcp/state.go` (no change to Save itself; just used as-is)
- Test: `internal/mcp/tools_test.go`

**Step 1: Write the failing test**

Append to `internal/mcp/tools_test.go`:

```go
func TestCreateSandboxPropagatesSaveError(t *testing.T) {
	tt, _ := newTestTools(t)
	// Force Save() to fail by making the state path unwritable.
	tt.State.path = "/nonexistent/dir/state.json"

	_, err := tt.CreateSandbox(context.Background(), CreateSandboxIn{})
	if err == nil {
		t.Fatal("expected CreateSandbox to surface the save error")
	}
	// The underlying backend.Create still ran; the test fake records that.
	// But state should reflect that we removed the entry on save failure.
	if got := len(tt.State.Sandboxes()); got != 0 {
		t.Errorf("state should be empty after save failure; got %d", got)
	}
}
```

> **Engineer note:** `tt.State.path` is unexported. Either temporarily export it for tests with `// for testing` doc, OR add a `WithPath(p string)` setter, OR make the test rely on `State.Save` failing some other way (e.g., write a directory at the state path so Rename fails). Pick whichever is least invasive — the existing test file already pokes at internals.

**Step 2: Verify failure**

```
go test ./internal/mcp -run TestCreateSandboxPropagatesSaveError -v
```

Expected: FAIL — current code swallows the error.

**Step 3: Implement**

In `internal/mcp/tools.go`, replace every `_ = t.State.Save()` site with a logged version. Add a helper:

```go
// persist saves state and logs save errors. Returns the error so callers can
// decide whether to surface it.
func (t *Tools) persist() error {
	if err := t.State.Save(); err != nil {
		t.log().Error("state save failed", "err", err)
		return err
	}
	return nil
}
```

For most sites (Stop, Start, Destroy, Exec, WriteFile, ReadFile, ListFiles, EditFile, DeleteFile), call `_ = t.persist()` — log but don't propagate. The container has already been mutated; the worst-case cost of a save failure is on-disk-divergent state, which the next successful save will correct.

For `CreateSandbox` specifically, propagate:

```go
// after t.State.Add(...)
if err := t.persist(); err != nil {
	// rollback in-memory state so the next call doesn't see a ghost
	t.State.Remove(name)
	// note: backend container still exists; the agent should retry destroy if they care
	return CreateSandboxOut{}, fmt.Errorf("create %s: state save failed: %w", name, err)
}
```

Same propagation in `StartSandbox` (since restart is also an inflection point — a save failure here means we'll think the sandbox is stopped on restart even though it's running).

For `BumpActivity`-only saves (the read-only-ish file ops), keep `_ = t.persist()` — the cost of losing a last_activity_at update on disk is at most one extra reaper-tick before the next op, harmless.

**Step 4: Run tests**

```
go test ./internal/mcp -v
```

Expected: PASS.

**Step 5: Commit**

```bash
jj describe -m "feat(mcp): surface State.Save errors via slog and propagate from CreateSandbox"
jj new
```

---

## Task 4: Wire `FilesViaExec` in `truenas.NewForTest` (Important #4 from review)

**Files:**
- Modify: `sandbox/truenas/truenas.go`
- Modify: `sandbox/truenas/truenas_test.go` (add interface assertion)

**Step 1: Write the failing test**

Append to `sandbox/truenas/truenas_test.go`:

```go
import "github.com/deevus/pixels/sandbox"

// Compile-time assertion: NewForTest must produce a *TrueNAS that satisfies sandbox.Sandbox.
var _ sandbox.Sandbox = func() sandbox.Sandbox {
	t, _ := NewForTest( /* whatever args NewForTest takes */ )
	return t
}()
```

> **Engineer note:** the assertion above is a thunk that runs at package init. If `NewForTest` requires args you can't provide statically, use a runtime test instead:

```go
func TestNewForTestSatisfiesFilesInterface(t *testing.T) {
	tn, _ := NewForTest( /* args */ )
	var _ sandbox.Files = tn  // compile-time check
	// Force a method call that would nil-panic if FilesViaExec were unwired.
	// Don't actually run it — just ensure the method set is present.
}
```

If `tn.FilesViaExec` is unset, calling any of WriteFile/ReadFile/ListFiles/DeleteFile would call methods on a nil-embedded value and panic. Add a runtime assertion that exercises one method on a sandbox whose Exec is a stub returning canned data, OR just visually verify the wiring after the fix.

**Step 2: Verify the bug**

Read `sandbox/truenas/truenas.go` `NewForTest`:

```bash
grep -n "FilesViaExec" sandbox/truenas/truenas.go
```

If the count of `FilesViaExec` references in that file is fewer than 2 (one in the struct embed, one in `New`), `NewForTest` is missing the wiring.

**Step 3: Add the wiring**

In `sandbox/truenas/truenas.go`, find `NewForTest`. After the struct is constructed, add:

```go
t.FilesViaExec = sandbox.FilesViaExec{Exec: t}
```

…matching the line that already exists in `New`.

**Step 4: Run tests**

```
go test ./sandbox/truenas -v
```

Expected: PASS.

**Step 5: Commit**

```bash
jj describe -m "fix(sandbox/truenas): wire FilesViaExec in NewForTest"
jj new
```

---

## Task 5: Add `transport_error` field to `ExecOut` (Important #5 from review)

**Files:**
- Modify: `internal/mcp/tools.go` (`ExecOut` struct + `Exec` handler)
- Test: `internal/mcp/tools_test.go`

**Step 1: Write the failing test**

```go
type errBackend struct {
	*fakeSandbox
	runErr error
}

func (e *errBackend) Run(ctx context.Context, name string, opts sandbox.ExecOpts) (int, error) {
	if opts.Stdout != nil {
		_, _ = opts.Stdout.Write([]byte("partial-stdout"))
	}
	return 137, e.runErr
}

func TestExecReportsTransportError(t *testing.T) {
	tt, _ := newTestTools(t)
	tt.Backend = &errBackend{
		fakeSandbox: tt.Backend.(*fakeSandbox),
		runErr:      errors.New("ssh connection lost"),
	}
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})

	res, err := tt.Exec(context.Background(), ExecIn{
		Name:    out.Name,
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatalf("Exec returned err=%v; expected the error in the response field instead", err)
	}
	if res.TransportError == "" {
		t.Errorf("expected non-empty TransportError; got %+v", res)
	}
	if res.ExitCode != 137 {
		t.Errorf("expected ExitCode propagated; got %d", res.ExitCode)
	}
	if res.Stdout != "partial-stdout" {
		t.Errorf("expected partial Stdout preserved; got %q", res.Stdout)
	}
}
```

(Add `"errors"` import.)

**Step 2: Verify failure**

```
go test ./internal/mcp -run TestExecReportsTransportError -v
```

Expected: FAIL — `TransportError` undefined or error returned.

**Step 3: Implement**

In `internal/mcp/tools.go`, add to `ExecOut`:

```go
type ExecOut struct {
	ExitCode       int    `json:"exit_code"`
	Stdout         string `json:"stdout"`
	Stderr         string `json:"stderr"`
	TransportError string `json:"transport_error,omitempty"` // non-empty when the underlying SSH/exec channel failed
}
```

In the `Exec` handler, replace the existing exit/err logic with:

```go
out := ExecOut{
	ExitCode: exit,
	Stdout:   stdout.String(),
	Stderr:   stderr.String(),
}
if err != nil {
	out.TransportError = err.Error()
}
return out, nil
```

That returns the structured failure info to the agent and never surfaces the error at the MCP transport layer. Agents that don't read `transport_error` get the same shape as today.

**Step 4: Run tests**

```
go test ./internal/mcp -v
```

Expected: PASS.

**Step 5: Commit**

```bash
jj describe -m "feat(mcp): surface transport errors via ExecOut.TransportError"
jj new
```

---

## Task 6: Conservative truncation on `stat` failure in `FilesViaExec.ReadFile` (Important #1 from review)

**Files:**
- Modify: `sandbox/filesexec.go`
- Test: `sandbox/filesexec_test.go`

**Step 1: Write the failing test**

Append to `sandbox/filesexec_test.go`:

```go
func TestFilesViaExecReadFileTruncatedOnStatError(t *testing.T) {
	fe := &fakeExec{
		runResponse: func(opts ExecOpts) (int, error) {
			if opts.Stdout != nil && len(opts.Cmd) > 0 && opts.Cmd[0] == "head" {
				_, _ = opts.Stdout.Write(bytes.Repeat([]byte("x"), 4))
			}
			return 0, nil
		},
		outputResp: func(cmd []string) ([]byte, error) {
			return nil, errors.New("stat: not allowed")
		},
	}
	files := FilesViaExec{Exec: fe}

	got, truncated, err := files.ReadFile(context.Background(), "sb", "/tmp/f", 4)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !truncated {
		t.Errorf("expected truncated=true when stat fails and read length == maxBytes")
	}
	if string(got) != "xxxx" {
		t.Errorf("got %q, want xxxx", got)
	}
}
```

(Add `"errors"` import.)

**Step 2: Verify failure**

```
go test ./sandbox -run TestFilesViaExecReadFileTruncatedOnStatError -v
```

Expected: FAIL — current code returns `truncated=false` when stat errors.

**Step 3: Implement**

In `sandbox/filesexec.go` `ReadFile`, replace the truncation block:

```go
truncated := false
if maxBytes > 0 && int64(buf.Len()) >= maxBytes {
	out, err := f.Exec.Output(ctx, name, []string{"stat", "-c", "%s", p})
	if err != nil {
		// stat failed; conservatively assume truncation. We read exactly
		// maxBytes, so the file is at least that big — treating it as
		// truncated is safe and visible to the caller.
		truncated = true
	} else if size, perr := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); perr == nil && size > maxBytes {
		truncated = true
	}
}
```

Note: the previous version had `--` between stat args and the path; if Task 7 hasn't landed yet, the stat call is unaffected (stat handles `--` correctly), but the find call in ListFiles is not. Task 7 deals with that.

**Step 4: Run tests**

```
go test ./sandbox -v
```

Expected: PASS.

**Step 5: Commit**

```bash
jj describe -m "fix(sandbox/filesexec): conservatively report truncated=true on stat failure"
jj new
```

---

## Task 7: Fix `list_files` — drop `--` from find argv (Bug 1)

**Files:**
- Modify: `sandbox/filesexec.go` (ListFiles)
- Test: `sandbox/filesexec_test.go`

**Step 1: Write the failing test**

Append to `sandbox/filesexec_test.go`:

```go
func TestFilesViaExecListFilesDoesNotPassDashDashToFind(t *testing.T) {
	var captured []string
	fe := &fakeExec{
		outputResp: func(cmd []string) ([]byte, error) {
			captured = cmd
			return []byte(""), nil
		},
	}
	files := FilesViaExec{Exec: fe}
	_, _ = files.ListFiles(context.Background(), "sb", "/tmp", false)

	for _, arg := range captured {
		if arg == "--" {
			t.Errorf("find argv should not contain --; got %v", captured)
		}
	}
}
```

**Step 2: Verify failure**

```
go test ./sandbox -run TestFilesViaExecListFilesDoesNotPassDashDashToFind -v
```

Expected: FAIL — current argv contains `--`.

**Step 3: Implement**

In `sandbox/filesexec.go` `ListFiles`, replace the args construction. From:

```go
args := []string{"find", "--", p, "-mindepth", "1", "-printf", "%p\t%s\t%m\t%y\n"}
if !recursive {
	args = []string{"find", "--", p, "-mindepth", "1", "-maxdepth", "1", "-printf", "%p\t%s\t%m\t%y\n"}
}
```

To:

```go
args := []string{"find", p, "-mindepth", "1", "-printf", "%p\t%s\t%m\t%y\n"}
if !recursive {
	args = []string{"find", p, "-mindepth", "1", "-maxdepth", "1", "-printf", "%p\t%s\t%m\t%y\n"}
}
```

If a path starting with `-` ever shows up, `find` will misinterpret it as an option. Validate at the MCP tool boundary (see Task 11 — Minor cleanup batch — for the `path.IsAbs` check).

**Step 4: Run tests**

```
go test ./sandbox -v
```

Expected: PASS.

**Step 5: Commit**

```bash
jj describe -m "fix(sandbox/filesexec): drop -- from find argv (Bug 1)"
jj new
```

---

## Task 8: Fix `exec` cwd and env via `env -C` wrapper (Bugs 3 + 4)

**Files:**
- Modify: `internal/mcp/tools.go` (Exec handler)
- Test: `internal/mcp/tools_test.go`

**Step 1: Write the failing test**

```go
func TestExecAppliesCwd(t *testing.T) {
	tt, fb := newTestTools(t)
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})

	// Capture the argv the backend sees.
	var capturedCmd []string
	fb.runHook = func(name string, opts sandbox.ExecOpts) (int, error) {
		capturedCmd = opts.Cmd
		return 0, nil
	}

	_, err := tt.Exec(context.Background(), ExecIn{
		Name:    out.Name,
		Command: []string{"pwd"},
		Cwd:     "/tmp",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"env", "-C", "/tmp", "--", "pwd"}
	if !reflect.DeepEqual(capturedCmd, want) {
		t.Errorf("exec argv = %v, want %v", capturedCmd, want)
	}
}

func TestExecAppliesEnv(t *testing.T) {
	tt, fb := newTestTools(t)
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})

	var capturedCmd []string
	fb.runHook = func(name string, opts sandbox.ExecOpts) (int, error) {
		capturedCmd = opts.Cmd
		return 0, nil
	}

	_, err := tt.Exec(context.Background(), ExecIn{
		Name:    out.Name,
		Command: []string{"sh", "-c", "echo $X"},
		Env:     map[string]string{"X": "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got, want := capturedCmd[0], "env"; got != want {
		t.Errorf("argv[0] = %q, want %q", got, want)
	}
	// Assert "X=hello" is somewhere in argv before "--".
	foundX := false
	for _, a := range capturedCmd {
		if a == "--" {
			break
		}
		if a == "X=hello" {
			foundX = true
		}
	}
	if !foundX {
		t.Errorf("env var X=hello missing from argv: %v", capturedCmd)
	}
}
```

(Add `"reflect"` import. Note `fb.runHook` is a new field on `fakeSandbox` for capturing Run calls. If it doesn't exist, add it now — the existing fake's Run is a no-op stub.)

**Step 2: Verify failure**

```
go test ./internal/mcp -run "TestExecApplies" -v
```

Expected: FAIL.

**Step 3: Implement**

In `internal/mcp/tools.go` `Exec`, replace the cwd-handling block. From whatever the current implementation has (probably nothing), to:

```go
// Wrap the command in `env [-C cwd] [KEY=val ...] -- <command>` so cwd and
// env take effect regardless of how the backend handles ExecOpts.Env.
prefix := []string{"env"}
if in.Cwd != "" {
	prefix = append(prefix, "-C", in.Cwd)
}
// Stable iteration so tests can assert on argv. Sort env keys.
keys := make([]string, 0, len(in.Env))
for k := range in.Env {
	keys = append(keys, k)
}
sort.Strings(keys)
for _, k := range keys {
	prefix = append(prefix, k+"="+in.Env[k])
}
prefix = append(prefix, "--")
cmd := append(prefix, in.Command...)

// Continue with the existing backend.Run call passing `cmd`.
exit, err := t.Backend.Run(ctx, sb.Name, sandbox.ExecOpts{
	Cmd:    cmd,
	Stdout: &stdout,
	Stderr: &stderr,
})
```

(Drop the `Env` field from `ExecOpts` — env is in the wrapped argv now. Or keep both, redundantly; the wrapped argv is the source of truth.)

(Add `"sort"` import.)

**Step 4: Run tests**

```
go test ./internal/mcp -v
```

Expected: PASS.

**Step 5: Commit**

```bash
jj describe -m "fix(mcp): wrap exec with 'env -C cwd KEY=val --' (Bugs 3 + 4)"
jj new
```

---

## Task 9: Fix README/design MCP config example (Bug — type:http) and refresh doc strings

**Files:**
- Modify: `README.md`
- Modify: `docs/plans/2026-04-27-mcp-sandbox-server-design.md`

**Step 1: Update README**

Find the JSON example block that shows `mcpServers.pixels` configuration. Replace:

```json
{
  "mcpServers": {
    "pixels": { "url": "http://127.0.0.1:8765/mcp" }
  }
}
```

with:

```json
{
  "mcpServers": {
    "pixels": {
      "type": "http",
      "url": "http://127.0.0.1:8765/mcp"
    }
  }
}
```

**Step 2: Update design doc**

Same fix in `docs/plans/2026-04-27-mcp-sandbox-server-design.md` Configuration section. Search for the JSON snippet and apply the same change.

**Step 3: Verify rendering**

```
grep -n '"type": "http"' README.md docs/plans/2026-04-27-mcp-sandbox-server-design.md
```

Expected: at least one match in each file.

**Step 4: Commit**

```bash
jj describe -m "docs: add type: http to MCP client config example"
jj new
```

---

## Task 10: Investigate and fix exec stderr capture (Bug 2)

**Files:**
- Investigation: `sandbox/incus/incus.go` and `sandbox/truenas/truenas.go` Run implementations
- Likely modify: one of the above
- Test: `sandbox/incus/incus_test.go` or `sandbox/truenas/truenas_test.go`

**Why this task is shaped differently:** the bug is in the backend, but we don't know which backend or which line until we read. This is an investigation-then-fix, not a TDD reach.

**Step 1: Investigate**

```bash
grep -n "Run\|Stderr\|stderr" sandbox/incus/incus.go | head -30
grep -n "Run\|Stderr\|stderr" sandbox/truenas/truenas.go | head -30
```

The user reports stderr is consistently `"" or "\n"` regardless of input. The MCP tool's `Tools.Exec` definitely passes a `*strings.Builder` as `ExecOpts.Stderr` (see Task 5 changes). So the backend's `Run` method must be:

(a) ignoring `opts.Stderr` and writing stderr to `opts.Stdout` instead, OR
(b) reading from a single muxed stream and splitting on a control byte that almost never fires for short messages, OR
(c) closing the stderr channel before the writer drains.

Find the implementation. Common pattern in Incus client: `incus.Operation` returns separate stdout/stderr WebSocket channels — the backend probably forwards channel 1 (stdout) but drops channel 2 (stderr). That would match the symptom (`"\n"` is the trailing newline from the channel close).

**Step 2: Write the failing test**

Once the implementation is read, write a test using the mock Incus / TrueNAS client that asserts stderr is captured. Pseudocode:

```go
func TestRunCapturesStderr(t *testing.T) {
	be := newBackendForTest(t)
	var stdout, stderr bytes.Buffer
	exit, err := be.Run(context.Background(), "test-sandbox", sandbox.ExecOpts{
		Cmd:    []string{"sh", "-c", "echo to-stderr >&2; echo to-stdout"},
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if exit != 0 {
		t.Errorf("exit = %d, want 0", exit)
	}
	if !strings.Contains(stdout.String(), "to-stdout") {
		t.Errorf("stdout missing 'to-stdout'; got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "to-stderr") {
		t.Errorf("stderr missing 'to-stderr'; got %q", stderr.String())
	}
}
```

For Incus this likely needs the integration-test setup against a real Incus daemon (no mock for the relevant channels). For TrueNAS, the existing `mock` server in `truenas-go` may or may not support arbitrary exec; check what's there. If neither backend has unit-testable Run, write the test as `t.Skip("requires live backend")` and verify manually.

**Step 3: Implement**

The fix depends on what step 1 turned up. The most likely shape:

- Incus exec opens four WebSocket channels (control, stdin, stdout, stderr) per the LXD/Incus exec spec. The backend probably wires control + stdout + stdin to the user but forgets stderr. Add the missing wire:

```go
// pseudo — adapt to actual incus client API
go func() {
	defer stderrConn.Close()
	if _, err := io.Copy(opts.Stderr, stderrConn); err != nil {
		// log via logger
	}
}()
```

- For TrueNAS, exec may go through a different path entirely (REST API or shelling out). Check whether `truenas-go.Exec` exposes stderr separately and plumb it.

**Step 4: Run tests + manual smoke**

```
go test ./sandbox/... -v
```

Manually with a live backend:

```
echo "" | pixels exec test-sandbox -- bash -c 'echo to-stderr >&2'
```

Should print `to-stderr` to the pixels CLI's stderr. If exec was already using the same backend code path, the manual smoke confirms the fix at both layers.

**Step 5: Commit**

```bash
jj describe -m "fix(sandbox): capture stderr in backend Run (Bug 2)"
jj new
```

---

## Task 11: Minor cleanup batch (review minors)

This is a single commit covering multiple small fixes. None of them individually warrant a TDD cycle.

**Files:**
- Modify: `cmd/mcp.go` — fix `isLoopback` for IPv6
- Modify: `internal/mcp/tools.go` — simplify `parseMode`
- Modify: `internal/mcp/server.go` — drop unused `*Tools` from public signature OR document
- Modify: `internal/mcp/reaper.go` — fix lazy `Now` race
- Modify: `sandbox/filesexec.go` — chmod-via-install (optional; defer if it complicates testing)
- Modify: `internal/mcp/tools_test.go` — add EditFile-on-missing-file test
- Modify: `internal/mcp/reaper_test.go` — add error-path test

**Step 1: Apply each fix**

(a) `cmd/mcp.go` `isLoopback`:

```go
import "net"

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
```

(b) `internal/mcp/tools.go` `parseMode` — drop the prefix stripping; `ParseUint(s, 8, 32)` already accepts `"0644"` natively in base 8:

```go
func parseMode(s string, fallback os.FileMode) os.FileMode {
	if s == "" {
		return fallback
	}
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return fallback
	}
	return os.FileMode(n)
}
```

(c) `internal/mcp/server.go` — change return signature to `http.Handler` only and document the test-only access to `*Tools` via a separate exported helper:

```go
// NewServer returns an HTTP handler. The embedded *Tools is exposed only via
// NewServerWithTools for tests that need to inject dependencies.
func NewServer(opts ServerOpts, endpointPath string) http.Handler {
	mux, _ := newServerImpl(opts, endpointPath)
	return mux
}

func newServerImpl(opts ServerOpts, endpointPath string) (http.Handler, *Tools) {
	// existing body
}

// NewServerWithTools is for tests only.
func NewServerWithTools(opts ServerOpts, endpointPath string) (http.Handler, *Tools) {
	return newServerImpl(opts, endpointPath)
}
```

Or simply leave the existing `(http.Handler, *Tools)` shape and add a doc comment that the second return is a test affordance. The cleaner refactor is preferred.

(d) `internal/mcp/reaper.go` lazy `Now`:

Replace:

```go
func (r *Reaper) Tick(ctx context.Context) {
	if r.Now == nil {
		r.Now = time.Now
	}
	now := r.Now()
	// ...
}
```

with a non-mutating helper:

```go
func (r *Reaper) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reaper) Tick(ctx context.Context) {
	now := r.now()
	// ...
}
```

(e) `sandbox/filesexec.go` chmod ordering — defer if it complicates `FilesViaExec` testing. The fix is to use `install -m <mode>` which is atomic, but `install` isn't always present on minimal containers. Pragmatic: leave chmod-after-write for now; document the small race window. **SKIP THIS SUB-FIX for v1; promote to its own task if it bites.**

(f) `internal/mcp/tools_test.go` — add test for EditFile on a file that doesn't exist:

```go
func TestEditFileMissingFileErrors(t *testing.T) {
	tt, _ := newTestTools(t)
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})
	_, err := tt.EditFile(context.Background(), EditFileIn{
		Name:      out.Name,
		Path:      "/does/not/exist",
		OldString: "anything",
		NewString: "x",
	})
	if err == nil {
		t.Fatal("expected EditFile to error when file is missing")
	}
}
```

(g) `internal/mcp/reaper_test.go` — add test for the Stop-error continue path:

```go
func TestReaperContinuesAfterStopError(t *testing.T) {
	dir := t.TempDir()
	s, _ := LoadState(filepath.Join(dir, "s.json"))
	now := time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC)
	s.Add(Sandbox{Name: "fail", Status: "running", CreatedAt: now.Add(-2 * time.Hour), LastActivityAt: now.Add(-90 * time.Minute)})
	s.Add(Sandbox{Name: "ok", Status: "running", CreatedAt: now.Add(-2 * time.Hour), LastActivityAt: now.Add(-90 * time.Minute)})

	be := &fakeBackend{}
	be.stopErr = errors.New("backend gone")  // applies to all stops
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
```

**Step 2: Run all tests**

```
go test ./... -race
go vet ./...
```

Expected: PASS, no vet warnings.

**Step 3: Commit**

```bash
jj describe -m "chore(mcp): minor cleanup batch from code review"
jj new
```

---

## Final cleanup

**Step 1: Run the full matrix**

```
go test ./... -race
go build ./...
go vet ./...
grep -rn "TODO\|FIXME\|panic(\"not implemented\")" internal/mcp sandbox/filesexec.go cmd/mcp.go
```

All four should be clean.

**Step 2: Smoke-test the daemon**

```
go run . mcp --verbose --listen-addr 127.0.0.1:9999
```

In another terminal:

```
curl -i http://127.0.0.1:9999/mcp
```

Watch the daemon output for verbose logs. Any panics, races, or unexpected stderr noise → triage before declaring done.

**Step 3: Final commit only if anything changed**

```bash
jj describe -m "chore(mcp): final hardening cleanup"
jj new
```

Otherwise nothing to do.

---

## Out of scope for this plan

- Async create_sandbox / provisioning state — covered by Plan B.
- Base-pixel lifecycle — covered by Plan B.
- Image-arg deprecation — handled by Plan B (Task 8 there obviates this concern).
- Refcounted lock pruning — only revisit if measured memory pressure (see locks.go doc comment).
- chmod-via-install for atomic write+mode — track separately if the race window bites.

If you find yourself wanting one of these, stop and check with the user first.
