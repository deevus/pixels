# MCP Sandbox Server Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a `pixels mcp` subcommand that runs a streamable-HTTP MCP server, exposing pixels container lifecycle plus exec, file CRUD, and a Claude-style `edit_file` as MCP tools, so AI agents can drive disposable Linux sandboxes.

**Architecture:** Long-running daemon, single instance per host (pidfile-gated), bound to `127.0.0.1` by default. Tool handlers call into the existing `sandbox.Sandbox` interface. File I/O is added as a new `Files` capability on that interface; both backends embed a shared `FilesViaExec` helper that composes `cat` / `head` / `find` / `rm` over the existing Exec transport. State persisted to a JSON file with atomic writes; per-sandbox mutexes serialize concurrent calls. See `docs/plans/2026-04-27-mcp-sandbox-server-design.md` for the design write-up.

**Tech Stack:**
- Go 1.25
- `github.com/modelcontextprotocol/go-sdk` — official MCP Go SDK (streamable-HTTP)
- `github.com/BurntSushi/toml` + `github.com/caarlos0/env/v11` — existing config pattern
- `github.com/spf13/cobra` — existing CLI
- `al.essio.dev/pkg/shellescape` — already used; safe quoting in `FilesViaExec`
- Existing packages: `internal/config`, `sandbox`, `sandbox/truenas`, `sandbox/incus`

No new SSH/SFTP dependencies. File ops ride the same Exec transport `pixels exec` already uses.

---

## Engineer-orientation notes

Before starting:

1. **Read the design doc** at `docs/plans/2026-04-27-mcp-sandbox-server-design.md`. Every architectural decision in this plan is justified there.
2. **Read `sandbox/sandbox.go`** to understand the `Sandbox` composite interface and the `Backend` / `Exec` / `NetworkPolicy` sub-interfaces it composes. The MCP tool handlers call into `Sandbox`, not into backend-specific code.
3. **Read `internal/config/config.go`** to understand the config-loading pattern (TOML defaults → file → env-var overrides via `caarlos0/env`).
4. **Read `cmd/root.go`** to understand how cobra commands access config and open a sandbox via `openSandbox()`.
5. **Verify the MCP Go SDK API** before Task 11 with `go doc github.com/modelcontextprotocol/go-sdk/mcp`. The SDK is young; check the current `mcp.NewServer` / `AddTool` / `StreamableHTTPHandler` shape and adapt the wrappers if signatures have shifted. The data flow is the same regardless.

**Commit cadence:** one commit per task. Each task is small enough that a single-line commit message is enough.

**Test conventions:** table-driven where possible (matches `cmd/resolve_test.go`, `internal/ssh/ssh_test.go`). Mock-based tests for backend-dependent code.

---

## Task 1: Add `[mcp]` config struct and defaults

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go` (create if absent)

**Step 1: Write the failing test**

Append to `internal/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMCPDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got, want := cfg.MCP.Prefix, "px-mcp-"; got != want {
		t.Errorf("Prefix = %q, want %q", got, want)
	}
	if got, want := cfg.MCP.IdleStopAfter, "1h"; got != want {
		t.Errorf("IdleStopAfter = %q, want %q", got, want)
	}
	if got, want := cfg.MCP.HardDestroyAfter, "24h"; got != want {
		t.Errorf("HardDestroyAfter = %q, want %q", got, want)
	}
	if got, want := cfg.MCP.ListenAddr, "127.0.0.1:8765"; got != want {
		t.Errorf("ListenAddr = %q, want %q", got, want)
	}
}

func TestMCPEnvOverride(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("PIXELS_MCP_LISTEN_ADDR", "0.0.0.0:9000")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.MCP.ListenAddr, "0.0.0.0:9000"; got != want {
		t.Errorf("ListenAddr = %q, want %q", got, want)
	}
}

func TestMCPTOMLOverride(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	cfgPath := filepath.Join(tmpDir, "pixels", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(`
[mcp]
prefix = "test-"
idle_stop_after = "30m"
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.MCP.Prefix, "test-"; got != want {
		t.Errorf("Prefix = %q, want %q", got, want)
	}
	if got, want := cfg.MCP.IdleStopAfter, "30m"; got != want {
		t.Errorf("IdleStopAfter = %q, want %q", got, want)
	}
}
```

**Step 2: Run tests to verify failure**

```
go test ./internal/config -run TestMCP -v
```

Expected: FAIL — `cfg.MCP` undefined.

**Step 3: Add the `MCP` struct and defaults**

In `internal/config/config.go`, add the `MCP` field to `Config` and a new struct:

```go
// add to Config struct
MCP        MCP            `toml:"mcp"`

// new struct
type MCP struct {
	Prefix           string `toml:"prefix"             env:"PIXELS_MCP_PREFIX"`
	DefaultImage     string `toml:"default_image"      env:"PIXELS_MCP_DEFAULT_IMAGE"`
	IdleStopAfter    string `toml:"idle_stop_after"    env:"PIXELS_MCP_IDLE_STOP_AFTER"`
	HardDestroyAfter string `toml:"hard_destroy_after" env:"PIXELS_MCP_HARD_DESTROY_AFTER"`
	ReapInterval     string `toml:"reap_interval"      env:"PIXELS_MCP_REAP_INTERVAL"`
	StateFile        string `toml:"state_file"         env:"PIXELS_MCP_STATE_FILE"`
	PIDFile          string `toml:"pid_file"           env:"PIXELS_MCP_PID_FILE"`
	ExecTimeoutMax   string `toml:"exec_timeout_max"   env:"PIXELS_MCP_EXEC_TIMEOUT_MAX"`
	ListenAddr       string `toml:"listen_addr"        env:"PIXELS_MCP_LISTEN_ADDR"`
	EndpointPath     string `toml:"endpoint_path"      env:"PIXELS_MCP_ENDPOINT_PATH"`
}
```

In `Load()`, set defaults inside the initial `cfg := &Config{ ... }` block:

```go
MCP: MCP{
	Prefix:           "px-mcp-",
	IdleStopAfter:    "1h",
	HardDestroyAfter: "24h",
	ReapInterval:     "1m",
	ExecTimeoutMax:   "10m",
	ListenAddr:       "127.0.0.1:8765",
	EndpointPath:     "/mcp",
	// DefaultImage falls back to cfg.Defaults.Image at use time.
	// StateFile, PIDFile resolved from XDG cache dir at use time.
},
```

**Step 4: Run tests to verify pass**

```
go test ./internal/config -run TestMCP -v
go build ./...
```

Expected: PASS, build clean.

**Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
jj commit -m "feat(config): add [mcp] config section with defaults"
```

---

## Task 2: Add MCP cache-path helpers

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Why:** State file and pidfile default to `~/.cache/pixels/`. Centralize the resolution so the MCP package doesn't reach into config internals.

**Step 1: Write the failing test**

Append:

```go
func TestMCPStateFilePath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	cfg := &Config{}
	got := cfg.MCPStateFile()
	want := filepath.Join(tmpDir, "pixels", "mcp-state.json")
	if got != want {
		t.Errorf("MCPStateFile = %q, want %q", got, want)
	}
}

func TestMCPStateFilePathOverride(t *testing.T) {
	cfg := &Config{MCP: MCP{StateFile: "/custom/state.json"}}
	if got, want := cfg.MCPStateFile(), "/custom/state.json"; got != want {
		t.Errorf("MCPStateFile = %q, want %q", got, want)
	}
}

func TestMCPPIDFilePath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	cfg := &Config{}
	got := cfg.MCPPIDFile()
	want := filepath.Join(tmpDir, "pixels", "mcp.pid")
	if got != want {
		t.Errorf("MCPPIDFile = %q, want %q", got, want)
	}
}
```

**Step 2: Verify failure**

```
go test ./internal/config -run "TestMCP.*Path|TestMCPPID" -v
```

Expected: FAIL — methods undefined.

**Step 3: Implement helpers**

Add to `internal/config/config.go`:

```go
// MCPStateFile returns the resolved path to the MCP state file.
func (c *Config) MCPStateFile() string {
	if c.MCP.StateFile != "" {
		return expandHome(c.MCP.StateFile)
	}
	return filepath.Join(mcpCacheDir(), "mcp-state.json")
}

// MCPPIDFile returns the resolved path to the MCP pidfile.
func (c *Config) MCPPIDFile() string {
	if c.MCP.PIDFile != "" {
		return expandHome(c.MCP.PIDFile)
	}
	return filepath.Join(mcpCacheDir(), "mcp.pid")
}

func mcpCacheDir() string {
	if dir := os.Getenv("XDG_CACHE_HOME"); dir != "" {
		return filepath.Join(dir, "pixels")
	}
	dir, _ := os.UserCacheDir()
	return filepath.Join(dir, "pixels")
}
```

**Step 4: Run tests**

```
go test ./internal/config -v
```

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
jj commit -m "feat(config): add MCPStateFile/MCPPIDFile path helpers"
```

---

## Task 3: Create `internal/mcp` package skeleton + state types

**Files:**
- Create: `internal/mcp/state.go`
- Create: `internal/mcp/state_test.go`

**Step 1: Write the failing test**

Create `internal/mcp/state_test.go`:

```go
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
```

**Step 2: Verify failure**

```
go test ./internal/mcp -v
```

Expected: FAIL — package or types undefined.

**Step 3: Implement `internal/mcp/state.go`**

```go
// Package mcp implements an MCP server that exposes pixels sandbox
// lifecycle, exec, and file I/O as tools for AI agents.
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Sandbox is a single tracked MCP-managed sandbox.
type Sandbox struct {
	Name           string    `json:"name"`
	Label          string    `json:"label,omitempty"`
	Image          string    `json:"image"`
	IP             string    `json:"ip,omitempty"`
	Status         string    `json:"status"` // "running" | "stopped"
	CreatedAt      time.Time `json:"created_at"`
	LastActivityAt time.Time `json:"last_activity_at"`
}

// State is the in-memory + on-disk MCP state.
type State struct {
	path string
	mu   sync.RWMutex
	data stateData
}

type stateData struct {
	Sandboxes []Sandbox `json:"sandboxes"`
}

// LoadState reads state from path. Missing or corrupt files yield an empty state.
func LoadState(path string) (*State, error) {
	s := &State{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading state: %w", err)
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		fmt.Fprintf(os.Stderr, "pixels mcp: state file corrupt, starting empty: %v\n", err)
		s.data = stateData{}
	}
	return s, nil
}

// Sandboxes returns a copy of the current sandbox slice.
func (s *State) Sandboxes() []Sandbox {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Sandbox, len(s.data.Sandboxes))
	copy(out, s.data.Sandboxes)
	return out
}

// Get returns a sandbox by name and whether it was found.
func (s *State) Get(name string) (Sandbox, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sb := range s.data.Sandboxes {
		if sb.Name == name {
			return sb, true
		}
	}
	return Sandbox{}, false
}

// Add inserts or replaces a sandbox.
func (s *State) Add(sb Sandbox) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.data.Sandboxes {
		if existing.Name == sb.Name {
			s.data.Sandboxes[i] = sb
			return
		}
	}
	s.data.Sandboxes = append(s.data.Sandboxes, sb)
}

// Remove deletes a sandbox by name. No-op if not present.
func (s *State) Remove(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.data.Sandboxes[:0]
	for _, sb := range s.data.Sandboxes {
		if sb.Name != name {
			out = append(out, sb)
		}
	}
	s.data.Sandboxes = out
}

// BumpActivity advances last_activity_at for the given sandbox. No-op if missing.
func (s *State) BumpActivity(name string, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Sandboxes {
		if s.data.Sandboxes[i].Name == name {
			s.data.Sandboxes[i].LastActivityAt = t
			return
		}
	}
}

// SetStatus updates a sandbox's status. No-op if missing.
func (s *State) SetStatus(name, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Sandboxes {
		if s.data.Sandboxes[i].Name == name {
			s.data.Sandboxes[i].Status = status
			return
		}
	}
}

// Save persists state atomically: write to <path>.tmp, then rename.
func (s *State) Save() error {
	s.mu.RLock()
	b, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write tmp state: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename state: %w", err)
	}
	return nil
}
```

**Step 4: Run**

```
go test ./internal/mcp -v
```

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/mcp/
jj commit -m "feat(mcp): add State type with in-memory + JSON persistence"
```

---

## Task 4: Add atomic-write and corruption tests for `State.Save`

**Files:**
- Modify: `internal/mcp/state_test.go`

**Step 1: Append tests**

```go
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

	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file should not exist after successful save")
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
```

(Add `"os"` import.)

**Step 2: Run + commit**

```
go test ./internal/mcp -v
```

Expected: PASS.

```bash
git add internal/mcp/state_test.go
jj commit -m "test(mcp): cover atomic save and corrupt-state recovery"
```

---

## Task 5: Implement pidfile (single-instance lock)

**Files:**
- Create: `internal/mcp/pidfile.go`
- Create: `internal/mcp/pidfile_test.go`

**Step 1: Write the failing test**

```go
package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquirePIDFileSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.pid")
	pf, err := AcquirePIDFile(path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer pf.Release()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pidfile: %v", err)
	}
	if got, want := string(b), fmt.Sprintf("%d\n", os.Getpid()); got != want {
		t.Errorf("pidfile content = %q, want %q", got, want)
	}
}

func TestAcquirePIDFileLiveCollision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.pid")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := AcquirePIDFile(path)
	if err == nil {
		t.Fatal("expected collision error")
	}
}

func TestAcquirePIDFileStalePID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.pid")
	if err := os.WriteFile(path, []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pf, err := AcquirePIDFile(path)
	if err != nil {
		t.Fatalf("acquire: %v (expected stale PID to be overwritten)", err)
	}
	defer pf.Release()
}

func TestPIDFileReleaseRemovesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.pid")
	pf, err := AcquirePIDFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := pf.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("pidfile should be removed after Release")
	}
}
```

**Step 2: Verify failure**

```
go test ./internal/mcp -run "TestAcquirePIDFile|TestPIDFile" -v
```

Expected: FAIL — undefined.

**Step 3: Implement**

```go
package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// PIDFile represents an acquired single-instance lock backed by a pidfile.
type PIDFile struct {
	path string
}

// AcquirePIDFile creates path with the current PID. Fails if path exists and
// the recorded PID is alive. Stale PIDs are overwritten.
func AcquirePIDFile(path string) (*PIDFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create pidfile dir: %w", err)
	}

	if existing, err := os.ReadFile(path); err == nil {
		pidStr := strings.TrimSpace(string(existing))
		if pid, err := strconv.Atoi(pidStr); err == nil && pidAlive(pid) {
			return nil, fmt.Errorf("another pixels mcp is running (pid=%d, pidfile=%s)", pid, path)
		}
	}

	content := fmt.Sprintf("%d\n", os.Getpid())
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return nil, fmt.Errorf("write pidfile: %w", err)
	}
	return &PIDFile{path: path}, nil
}

// Release removes the pidfile. Safe to call multiple times.
func (p *PIDFile) Release() error {
	err := os.Remove(p.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// pidAlive reports whether a process with the given pid exists.
// On Unix, signal 0 is the standard "is this PID alive" check.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
```

**Step 4: Run + commit**

```
go test ./internal/mcp -v
```

Expected: PASS.

```bash
git add internal/mcp/
jj commit -m "feat(mcp): add pidfile with stale-PID detection"
```

---

## Task 6: Add `Files` capability to the `sandbox` package

**Files:**
- Modify: `sandbox/sandbox.go`
- Test: none yet (the interface itself has no behavior; behavior arrives in Task 7).

**Why:** The MCP server needs file CRUD on sandbox containers. Rather than have the MCP layer compose shell commands itself, give backends a clean interface to implement (and a default implementation in Task 7).

**Step 1: Add the `Files` interface and `FileEntry` type**

In `sandbox/sandbox.go`:

```go
// Add near the existing FileEntry-shaped types (after NetworkPolicy).
// FileEntry describes one entry in a ListFiles result.
type FileEntry struct {
	Path  string      `json:"path"`
	Size  int64       `json:"size"`
	Mode  os.FileMode `json:"mode"`
	IsDir bool        `json:"is_dir"`
}

// Files provides byte-level file I/O into a sandbox instance.
type Files interface {
	WriteFile(ctx context.Context, name, path string, content []byte, mode os.FileMode) error
	ReadFile(ctx context.Context, name, path string, maxBytes int64) (content []byte, truncated bool, err error)
	ListFiles(ctx context.Context, name, path string, recursive bool) ([]FileEntry, error)
	DeleteFile(ctx context.Context, name, path string) error
}

// Sandbox composes all sandbox capabilities into a single interface.
type Sandbox interface {
	Backend
	Exec
	Files          // NEW
	NetworkPolicy
}
```

Add `"os"` to imports (needed for `os.FileMode`).

**Step 2: Run build**

```
go build ./...
```

Expected: FAIL — `*incus.Incus` and `*truenas.TrueNAS` don't satisfy `Sandbox` anymore. The compile error confirms the interface change took effect; Tasks 7–9 fix the backends.

> **Engineer note:** the assertion lines `var _ sandbox.Sandbox = (*TrueNAS)(nil)` and `var _ sandbox.Sandbox = (*Incus)(nil)` in the backends will fail to compile. That's expected. **Do not commit yet** — proceed to Task 7.

---

## Task 7: Implement the `FilesViaExec` helper

**Files:**
- Create: `sandbox/filesexec.go`
- Create: `sandbox/filesexec_test.go`

**Why:** Backends without a native file API (TrueNAS, possibly others later) get a default implementation by composing `cat` / `head` / `find` / `rm` over `Exec.Run`. Embedding `FilesViaExec` in a backend struct gives it `Files` for free.

**Step 1: Write the failing test**

Create `sandbox/filesexec_test.go`:

```go
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeExec captures Run/Output calls. ReadFile etc. work by inspecting the
// command and either writing to the provided Stdout, returning canned bytes,
// or recording a side-effect.
type fakeExec struct {
	calls       []ExecOpts
	runResponse func(opts ExecOpts) (int, error)
	outputResp  func(cmd []string) ([]byte, error)
}

func (f *fakeExec) Run(ctx context.Context, name string, opts ExecOpts) (int, error) {
	f.calls = append(f.calls, opts)
	if f.runResponse != nil {
		return f.runResponse(opts)
	}
	return 0, nil
}
func (f *fakeExec) Output(ctx context.Context, name string, cmd []string) ([]byte, error) {
	if f.outputResp != nil {
		return f.outputResp(cmd)
	}
	return nil, errors.New("Output not stubbed")
}
func (f *fakeExec) Console(ctx context.Context, name string, opts ConsoleOpts) error { return nil }
func (f *fakeExec) Ready(ctx context.Context, name string, _ any) error               { return nil }

// helper: most recent call's command joined with spaces (for asserts)
func lastCmd(f *fakeExec) string {
	return strings.Join(f.calls[len(f.calls)-1].Cmd, " ")
}

func TestFilesViaExecWriteFile(t *testing.T) {
	fe := &fakeExec{}
	files := FilesViaExec{Exec: fe}

	if err := files.WriteFile(context.Background(), "sb", "/tmp/foo.txt", []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// First call: mkdir -p /tmp ; second: cat > /tmp/foo.txt ; third: chmod 644 /tmp/foo.txt
	if got := len(fe.calls); got < 2 {
		t.Fatalf("calls = %d, want >= 2: %+v", got, fe.calls)
	}
	// Find the cat write call and verify Stdin matches content.
	var found bool
	for _, c := range fe.calls {
		if len(c.Cmd) >= 2 && c.Cmd[0] == "sh" && strings.Contains(strings.Join(c.Cmd, " "), "cat") {
			if c.Stdin == nil {
				t.Fatal("write call has nil Stdin")
			}
			b, _ := readAll(c.Stdin)
			if string(b) != "hello" {
				t.Errorf("written content = %q, want %q", b, "hello")
			}
			found = true
		}
	}
	if !found {
		t.Errorf("no cat-write call found in: %+v", fe.calls)
	}
}

func TestFilesViaExecReadFileFull(t *testing.T) {
	fe := &fakeExec{
		runResponse: func(opts ExecOpts) (int, error) {
			if opts.Stdout != nil && len(opts.Cmd) > 0 && opts.Cmd[0] == "cat" {
				_, _ = opts.Stdout.Write([]byte("hi"))
			}
			return 0, nil
		},
	}
	files := FilesViaExec{Exec: fe}

	got, truncated, err := files.ReadFile(context.Background(), "sb", "/tmp/f", 0)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if truncated {
		t.Error("truncated should be false when maxBytes==0")
	}
	if string(got) != "hi" {
		t.Errorf("got %q, want %q", got, "hi")
	}
}

func TestFilesViaExecReadFileTruncated(t *testing.T) {
	fe := &fakeExec{
		runResponse: func(opts ExecOpts) (int, error) {
			if opts.Stdout != nil && len(opts.Cmd) > 0 && opts.Cmd[0] == "head" {
				_, _ = opts.Stdout.Write(bytes.Repeat([]byte("x"), 4))
			}
			return 0, nil
		},
		outputResp: func(cmd []string) ([]byte, error) {
			// stat -c %s -> file is 10 bytes, > maxBytes(4) => truncated
			return []byte("10\n"), nil
		},
	}
	files := FilesViaExec{Exec: fe}

	got, truncated, err := files.ReadFile(context.Background(), "sb", "/tmp/f", 4)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !truncated {
		t.Error("truncated should be true (file=10, maxBytes=4)")
	}
	if string(got) != "xxxx" {
		t.Errorf("got %q, want xxxx", got)
	}
}

func TestFilesViaExecListFiles(t *testing.T) {
	fe := &fakeExec{
		outputResp: func(cmd []string) ([]byte, error) {
			// find -printf '%p\t%s\t%m\t%y\n'
			return []byte("/tmp/a.txt\t3\t644\tf\n/tmp/sub\t4096\t755\td\n"), nil
		},
	}
	files := FilesViaExec{Exec: fe}

	entries, err := files.ListFiles(context.Background(), "sb", "/tmp", false)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if got := len(entries); got != 2 {
		t.Fatalf("entries = %d, want 2: %+v", got, entries)
	}
	if entries[1].IsDir != true {
		t.Errorf("entries[1].IsDir = false, want true")
	}
}

func TestFilesViaExecDeleteFile(t *testing.T) {
	fe := &fakeExec{}
	files := FilesViaExec{Exec: fe}

	if err := files.DeleteFile(context.Background(), "sb", "/tmp/f"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if got := lastCmd(fe); !strings.HasPrefix(got, "rm ") || !strings.Contains(got, "/tmp/f") {
		t.Errorf("last cmd = %q, want rm of /tmp/f", got)
	}
}
```

(Add `"io"` import to silence the unused-package check. Or define `readAll = io.ReadAll` if needed.)

**Step 2: Verify failure**

```
go test ./sandbox -v
```

Expected: FAIL — `FilesViaExec` undefined.

**Step 3: Implement `sandbox/filesexec.go`**

```go
package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"

	shellescape "al.essio.dev/pkg/shellescape"
)

// FilesViaExec implements [Files] by composing shell commands over an [Exec].
// Backends without a native file API can embed this struct to satisfy [Files].
//
// All commands assume a POSIX shell with `cat`, `head`, `find`, `rm`,
// `mkdir`, `chmod`, and `stat` available — which is true on every Linux
// container image we ship.
type FilesViaExec struct {
	Exec Exec
}

// WriteFile creates parent dirs, streams content via stdin into `cat > path`,
// then chmods to mode.
func (f FilesViaExec) WriteFile(ctx context.Context, name, p string, content []byte, mode os.FileMode) error {
	if dir := path.Dir(p); dir != "." && dir != "/" {
		if code, err := f.Exec.Run(ctx, name, ExecOpts{
			Cmd: []string{"mkdir", "-p", "--", dir},
		}); err != nil || code != 0 {
			return fmt.Errorf("mkdir %s: code=%d err=%v", dir, code, err)
		}
	}

	var stderr bytes.Buffer
	cmd := fmt.Sprintf("cat > %s", shellescape.Quote(p))
	code, err := f.Exec.Run(ctx, name, ExecOpts{
		Cmd:    []string{"sh", "-c", cmd},
		Stdin:  bytes.NewReader(content),
		Stderr: &stderr,
	})
	if err != nil {
		return fmt.Errorf("write %s: %w", p, err)
	}
	if code != 0 {
		return fmt.Errorf("write %s: exit %d: %s", p, code, stderr.String())
	}

	if code, err := f.Exec.Run(ctx, name, ExecOpts{
		Cmd: []string{"chmod", fmt.Sprintf("%o", mode), "--", p},
	}); err != nil || code != 0 {
		return fmt.Errorf("chmod %s: code=%d err=%v", p, code, err)
	}
	return nil
}

// ReadFile streams the file (or first maxBytes) to a buffer. If maxBytes>0
// and the file is larger, returns truncated=true.
func (f FilesViaExec) ReadFile(ctx context.Context, name, p string, maxBytes int64) ([]byte, bool, error) {
	var buf bytes.Buffer
	var cmd []string
	if maxBytes > 0 {
		cmd = []string{"head", "-c", strconv.FormatInt(maxBytes, 10), "--", p}
	} else {
		cmd = []string{"cat", "--", p}
	}

	code, err := f.Exec.Run(ctx, name, ExecOpts{Cmd: cmd, Stdout: &buf})
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", p, err)
	}
	if code != 0 {
		return nil, false, fmt.Errorf("read %s: exit %d", p, code)
	}

	truncated := false
	if maxBytes > 0 && int64(buf.Len()) >= maxBytes {
		out, err := f.Exec.Output(ctx, name, []string{"stat", "-c", "%s", "--", p})
		if err == nil {
			if size, perr := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); perr == nil && size > maxBytes {
				truncated = true
			}
		}
	}
	return buf.Bytes(), truncated, nil
}

// ListFiles uses `find -printf '%p\t%s\t%m\t%y\n'` to enumerate entries.
// Non-recursive uses -maxdepth 1.
func (f FilesViaExec) ListFiles(ctx context.Context, name, p string, recursive bool) ([]FileEntry, error) {
	args := []string{"find", "--", p, "-mindepth", "1", "-printf", "%p\t%s\t%m\t%y\n"}
	if !recursive {
		args = []string{"find", "--", p, "-mindepth", "1", "-maxdepth", "1", "-printf", "%p\t%s\t%m\t%y\n"}
	}
	out, err := f.Exec.Output(ctx, name, args)
	if err != nil {
		return nil, fmt.Errorf("find %s: %w", p, err)
	}
	var entries []FileEntry
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) != 4 {
			continue
		}
		size, _ := strconv.ParseInt(parts[1], 10, 64)
		modeOct, _ := strconv.ParseUint(parts[2], 8, 32)
		entries = append(entries, FileEntry{
			Path:  parts[0],
			Size:  size,
			Mode:  os.FileMode(modeOct),
			IsDir: parts[3] == "d",
		})
	}
	return entries, nil
}

// DeleteFile removes a single file. Use `exec rm -rf` for recursive deletes.
func (f FilesViaExec) DeleteFile(ctx context.Context, name, p string) error {
	var stderr bytes.Buffer
	code, err := f.Exec.Run(ctx, name, ExecOpts{
		Cmd:    []string{"rm", "--", p},
		Stderr: &stderr,
	})
	if err != nil {
		return fmt.Errorf("rm %s: %w", p, err)
	}
	if code != 0 {
		return fmt.Errorf("rm %s: exit %d: %s", p, code, stderr.String())
	}
	return nil
}

// readAll is a small helper that lets test files reuse the same name without
// importing io directly. Production code uses io.ReadAll inline.
func readAll(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	return io.ReadAll(r)
}
```

> **Engineer note on the test fake's `Ready` signature:** the real `Exec.Ready` takes `time.Duration`. If the test compiler complains about the `any` placeholder, change `fakeExec.Ready` to `func (f *fakeExec) Ready(ctx context.Context, name string, _ time.Duration) error`. Adapt and re-run.

**Step 4: Run + commit**

```
go test ./sandbox -v
```

Expected: PASS.

```bash
git add sandbox/filesexec.go sandbox/filesexec_test.go sandbox/sandbox.go
jj commit -m "feat(sandbox): add Files capability and FilesViaExec helper"
```

---

## Task 8: Implement `Files` on the Incus backend

**Files:**
- Modify: `sandbox/incus/incus.go`

**Why:** Make `*Incus` satisfy the new `sandbox.Files` interface so `var _ sandbox.Sandbox = (*Incus)(nil)` compiles. We use the `FilesViaExec` helper rather than going to the Incus daemon's native file API in v1 — the helper is fast enough, and dropping in native file push/pull is a future optimization.

**Step 1: Embed `FilesViaExec`**

In `sandbox/incus/incus.go`, add the embedded helper field to the `Incus` struct:

```go
type Incus struct {
	// ... existing fields ...

	// Embedded helper provides WriteFile/ReadFile/ListFiles/DeleteFile via Run.
	sandbox.FilesViaExec
}
```

In `New(...)`, initialize the helper after the rest of the struct is built:

```go
func New(cfg map[string]string) (*Incus, error) {
	// ... existing construction ...

	i := &Incus{ /* existing field assignments */ }
	i.FilesViaExec = sandbox.FilesViaExec{Exec: i}
	return i, nil
}
```

> **Engineer note:** `i.FilesViaExec.Exec = i` is safe even though `i` is being constructed — Go interface values capture the pointer, and the interface is only invoked at call time when `i` is fully populated.

**Step 2: Build**

```
go build ./...
```

Expected: clean build for the Incus backend (TrueNAS still fails its `var _ sandbox.Sandbox = (*TrueNAS)(nil)` until Task 9).

If the build fails because of an existing assertion in the same file, leave it — we expect it. But the Incus assertion specifically should now compile.

**Step 3: Smoke test (optional but encouraged)**

If you have a working Incus config locally:

```
go run . list
go run . create test-mcp-files
go run . exec test-mcp-files -- "echo hi"
```

Skip if you don't have an Incus environment configured.

**Step 4: Commit**

```bash
git add sandbox/incus/incus.go
jj commit -m "feat(sandbox/incus): satisfy Files via embedded FilesViaExec"
```

---

## Task 9: Implement `Files` on the TrueNAS backend

**Files:**
- Modify: `sandbox/truenas/truenas.go`

**Step 1: Embed `FilesViaExec`**

Same pattern as Task 8, applied to `*TrueNAS`:

```go
type TrueNAS struct {
	// ... existing fields ...
	sandbox.FilesViaExec
}

func New(cfg map[string]string) (*TrueNAS, error) {
	// ... existing construction ...

	t := &TrueNAS{ /* existing field assignments */ }
	t.FilesViaExec = sandbox.FilesViaExec{Exec: t}
	return t, nil
}
```

**Step 2: Build all**

```
go build ./...
go test ./...
```

Expected: clean build, all existing tests still pass.

**Step 3: Commit**

```bash
git add sandbox/truenas/truenas.go
jj commit -m "feat(sandbox/truenas): satisfy Files via embedded FilesViaExec"
```

---

## Task 10: Implement reaper

**Files:**
- Create: `internal/mcp/reaper.go`
- Create: `internal/mcp/reaper_test.go`

**Step 1: Write the failing test**

```go
package mcp

import (
	"context"
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
```

**Step 2: Verify failure**

```
go test ./internal/mcp -run TestReaper -v
```

Expected: FAIL.

**Step 3: Implement `internal/mcp/reaper.go`**

```go
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
```

**Step 4: Run + commit**

```
go test ./internal/mcp -v
```

Expected: PASS.

```bash
git add internal/mcp/reaper.go internal/mcp/reaper_test.go
jj commit -m "feat(mcp): add reaper with idle-stop and hard-destroy TTLs"
```

---

## Task 11: Implement MCP tool layer

**Files:**
- Create: `internal/mcp/tools.go`
- Create: `internal/mcp/tools_test.go`

**Step 1: Define inputs/outputs and handlers**

Create `internal/mcp/tools.go`:

```go
package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deevus/pixels/sandbox"
)

// Tools is the dependency bundle every MCP handler closes over.
type Tools struct {
	State          *State
	Backend        sandbox.Sandbox
	Prefix         string
	DefaultImage   string
	ExecTimeoutMax time.Duration

	// Per-sandbox mutex map. Tool handlers acquire the lock for the duration
	// of any call that touches the container, so concurrent ops on the same
	// sandbox don't race.
	mu      sync.Mutex
	sbLocks map[string]*sync.Mutex
}

func (t *Tools) sbMutex(name string) *sync.Mutex {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sbLocks == nil {
		t.sbLocks = make(map[string]*sync.Mutex)
	}
	m, ok := t.sbLocks[name]
	if !ok {
		m = &sync.Mutex{}
		t.sbLocks[name] = m
	}
	return m
}

// --- Input/output types ---

type CreateSandboxIn struct {
	Label string `json:"label,omitempty"`
	Image string `json:"image,omitempty"`
}
type CreateSandboxOut struct {
	Name   string `json:"name"`
	IP     string `json:"ip"`
	Status string `json:"status"`
}

type SandboxRef struct {
	Name string `json:"name"`
}
type Ack struct {
	OK bool `json:"ok"`
}

type ListSandboxesOut struct {
	Sandboxes []SandboxView `json:"sandboxes"`
}
type SandboxView struct {
	Name           string    `json:"name"`
	Label          string    `json:"label,omitempty"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	LastActivityAt time.Time `json:"last_activity_at"`
	IdleFor        string    `json:"idle_for"`
}

type ExecIn struct {
	Name       string            `json:"name"`
	Command    []string          `json:"command"`
	Cwd        string            `json:"cwd,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	TimeoutSec int               `json:"timeout_sec,omitempty"`
}
type ExecOut struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

type WriteFileIn struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    string `json:"mode,omitempty"` // octal string e.g. "0644"
}
type WriteFileOut struct {
	OK           bool `json:"ok"`
	BytesWritten int  `json:"bytes_written"`
}

type ReadFileIn struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}
type ReadFileOut struct {
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

type ListFilesIn struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Recursive bool   `json:"recursive,omitempty"`
}
type ListFilesOut struct {
	Entries []sandbox.FileEntry `json:"entries"`
}

type EditFileIn struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}
type EditFileOut struct {
	OK           bool `json:"ok"`
	Replacements int  `json:"replacements"`
}

type DeleteFileIn struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// --- Helpers ---

func (t *Tools) generateName() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return t.Prefix + hex.EncodeToString(b[:])
}

func (t *Tools) requireSandbox(name string) (Sandbox, error) {
	sb, ok := t.State.Get(name)
	if !ok {
		return sb, fmt.Errorf("sandbox %q not found", name)
	}
	return sb, nil
}

func parseMode(s string, fallback os.FileMode) os.FileMode {
	if s == "" {
		return fallback
	}
	s = strings.TrimPrefix(s, "0o")
	s = strings.TrimPrefix(s, "0")
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return fallback
	}
	return os.FileMode(n)
}

// --- Lifecycle handlers ---

func (t *Tools) CreateSandbox(ctx context.Context, in CreateSandboxIn) (CreateSandboxOut, error) {
	image := in.Image
	if image == "" {
		image = t.DefaultImage
	}
	name := t.generateName()
	inst, err := t.Backend.Create(ctx, sandbox.CreateOpts{Name: name, Image: image})
	if err != nil {
		return CreateSandboxOut{}, fmt.Errorf("create: %w", err)
	}
	now := time.Now().UTC()
	ip := ""
	if len(inst.Addresses) > 0 {
		ip = inst.Addresses[0]
	}
	t.State.Add(Sandbox{
		Name:           name,
		Label:          in.Label,
		Image:          image,
		IP:             ip,
		Status:         "running",
		CreatedAt:      now,
		LastActivityAt: now,
	})
	_ = t.State.Save()
	return CreateSandboxOut{Name: name, IP: ip, Status: "running"}, nil
}

func (t *Tools) DestroySandbox(ctx context.Context, in SandboxRef) (Ack, error) {
	if err := t.Backend.Delete(ctx, in.Name); err != nil {
		return Ack{}, err
	}
	t.State.Remove(in.Name)
	_ = t.State.Save()
	return Ack{OK: true}, nil
}

func (t *Tools) StopSandbox(ctx context.Context, in SandboxRef) (Ack, error) {
	if err := t.Backend.Stop(ctx, in.Name); err != nil {
		return Ack{}, err
	}
	t.State.SetStatus(in.Name, "stopped")
	_ = t.State.Save()
	return Ack{OK: true}, nil
}

func (t *Tools) StartSandbox(ctx context.Context, in SandboxRef) (CreateSandboxOut, error) {
	if err := t.Backend.Start(ctx, in.Name); err != nil {
		return CreateSandboxOut{}, err
	}
	inst, err := t.Backend.Get(ctx, in.Name)
	if err != nil {
		return CreateSandboxOut{}, err
	}
	ip := ""
	if len(inst.Addresses) > 0 {
		ip = inst.Addresses[0]
	}
	t.State.SetStatus(in.Name, "running")
	t.State.BumpActivity(in.Name, time.Now().UTC())
	_ = t.State.Save()
	return CreateSandboxOut{Name: in.Name, IP: ip, Status: "running"}, nil
}

func (t *Tools) ListSandboxes(ctx context.Context) (ListSandboxesOut, error) {
	now := time.Now().UTC()
	in := t.State.Sandboxes()
	out := make([]SandboxView, 0, len(in))
	for _, sb := range in {
		out = append(out, SandboxView{
			Name:           sb.Name,
			Label:          sb.Label,
			Status:         sb.Status,
			CreatedAt:      sb.CreatedAt,
			LastActivityAt: sb.LastActivityAt,
			IdleFor:        now.Sub(sb.LastActivityAt).Round(time.Second).String(),
		})
	}
	return ListSandboxesOut{Sandboxes: out}, nil
}

// --- Exec handler ---

func (t *Tools) Exec(ctx context.Context, in ExecIn) (ExecOut, error) {
	sb, err := t.requireSandbox(in.Name)
	if err != nil {
		return ExecOut{}, err
	}
	timeout := time.Duration(in.TimeoutSec) * time.Second
	if timeout <= 0 || timeout > t.ExecTimeoutMax {
		timeout = t.ExecTimeoutMax
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	mu := t.sbMutex(sb.Name)
	mu.Lock()
	defer mu.Unlock()

	cmd := in.Command
	if in.Cwd != "" {
		cmd = []string{"sh", "-c", "cd \"$1\" && shift && exec \"$@\"", "_", in.Cwd}
		cmd = append(cmd, in.Command...)
	}

	envSlice := make([]string, 0, len(in.Env))
	for k, v := range in.Env {
		envSlice = append(envSlice, k+"="+v)
	}

	var stdout, stderr strings.Builder
	exit, err := t.Backend.Run(ctx, sb.Name, sandbox.ExecOpts{
		Cmd:    cmd,
		Env:    envSlice,
		Stdout: &stdout,
		Stderr: &stderr,
	})
	t.State.BumpActivity(sb.Name, time.Now().UTC())
	_ = t.State.Save()

	out := ExecOut{
		ExitCode: exit,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}
	if err != nil && exit == 0 {
		return out, err
	}
	return out, nil
}

// --- File handlers ---

func (t *Tools) WriteFile(ctx context.Context, in WriteFileIn) (WriteFileOut, error) {
	sb, err := t.requireSandbox(in.Name)
	if err != nil {
		return WriteFileOut{}, err
	}
	mode := parseMode(in.Mode, 0o644)
	mu := t.sbMutex(sb.Name)
	mu.Lock()
	defer mu.Unlock()

	if err := t.Backend.WriteFile(ctx, sb.Name, in.Path, []byte(in.Content), mode); err != nil {
		return WriteFileOut{}, err
	}
	t.State.BumpActivity(sb.Name, time.Now().UTC())
	_ = t.State.Save()
	return WriteFileOut{OK: true, BytesWritten: len(in.Content)}, nil
}

func (t *Tools) ReadFile(ctx context.Context, in ReadFileIn) (ReadFileOut, error) {
	sb, err := t.requireSandbox(in.Name)
	if err != nil {
		return ReadFileOut{}, err
	}
	mu := t.sbMutex(sb.Name)
	mu.Lock()
	defer mu.Unlock()

	body, truncated, err := t.Backend.ReadFile(ctx, sb.Name, in.Path, in.MaxBytes)
	if err != nil {
		return ReadFileOut{}, err
	}
	t.State.BumpActivity(sb.Name, time.Now().UTC())
	_ = t.State.Save()
	return ReadFileOut{Content: string(body), Truncated: truncated}, nil
}

func (t *Tools) ListFiles(ctx context.Context, in ListFilesIn) (ListFilesOut, error) {
	sb, err := t.requireSandbox(in.Name)
	if err != nil {
		return ListFilesOut{}, err
	}
	mu := t.sbMutex(sb.Name)
	mu.Lock()
	defer mu.Unlock()

	entries, err := t.Backend.ListFiles(ctx, sb.Name, in.Path, in.Recursive)
	if err != nil {
		return ListFilesOut{}, err
	}
	t.State.BumpActivity(sb.Name, time.Now().UTC())
	_ = t.State.Save()
	return ListFilesOut{Entries: entries}, nil
}

// EditFile is read-modify-write composed in the MCP layer. It mirrors the
// shape of Claude's Edit tool: errors if old_string is missing or non-unique
// (unless replace_all is true).
func (t *Tools) EditFile(ctx context.Context, in EditFileIn) (EditFileOut, error) {
	if in.OldString == "" {
		return EditFileOut{}, fmt.Errorf("old_string must not be empty")
	}
	if in.OldString == in.NewString {
		return EditFileOut{}, fmt.Errorf("old_string and new_string are identical")
	}
	sb, err := t.requireSandbox(in.Name)
	if err != nil {
		return EditFileOut{}, err
	}
	mu := t.sbMutex(sb.Name)
	mu.Lock()
	defer mu.Unlock()

	body, _, err := t.Backend.ReadFile(ctx, sb.Name, in.Path, 0)
	if err != nil {
		return EditFileOut{}, fmt.Errorf("read: %w", err)
	}
	original := string(body)
	count := strings.Count(original, in.OldString)
	switch {
	case count == 0:
		return EditFileOut{}, fmt.Errorf("old_string not found in %s", in.Path)
	case count > 1 && !in.ReplaceAll:
		return EditFileOut{}, fmt.Errorf("old_string appears %d times in %s; pass replace_all=true or include more context", count, in.Path)
	}

	var updated string
	if in.ReplaceAll {
		updated = strings.ReplaceAll(original, in.OldString, in.NewString)
	} else {
		updated = strings.Replace(original, in.OldString, in.NewString, 1)
	}

	// Preserve mode best-effort. ReadFile doesn't expose it, so default to 0644.
	// For finer fidelity we could ListFiles on the parent and look it up, but
	// 0644 is the right default for source files and matches what most editors
	// land on.
	if err := t.Backend.WriteFile(ctx, sb.Name, in.Path, []byte(updated), 0o644); err != nil {
		return EditFileOut{}, fmt.Errorf("write: %w", err)
	}
	t.State.BumpActivity(sb.Name, time.Now().UTC())
	_ = t.State.Save()
	return EditFileOut{OK: true, Replacements: count}, nil
}

func (t *Tools) DeleteFile(ctx context.Context, in DeleteFileIn) (Ack, error) {
	sb, err := t.requireSandbox(in.Name)
	if err != nil {
		return Ack{}, err
	}
	mu := t.sbMutex(sb.Name)
	mu.Lock()
	defer mu.Unlock()
	if err := t.Backend.DeleteFile(ctx, sb.Name, in.Path); err != nil {
		return Ack{}, err
	}
	t.State.BumpActivity(sb.Name, time.Now().UTC())
	_ = t.State.Save()
	return Ack{OK: true}, nil
}
```

**Step 2: Write a unit test for the in-memory bits**

Create `internal/mcp/tools_test.go`:

```go
package mcp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/deevus/pixels/sandbox"
)

type fakeSandbox struct {
	created []sandbox.CreateOpts
	deleted []string
	stopped []string
	started []string
	files   map[string][]byte
}

func newFakeSandbox() *fakeSandbox { return &fakeSandbox{files: make(map[string][]byte)} }

func (f *fakeSandbox) Create(ctx context.Context, o sandbox.CreateOpts) (*sandbox.Instance, error) {
	f.created = append(f.created, o)
	return &sandbox.Instance{Name: o.Name, Status: sandbox.StatusRunning, Addresses: []string{"10.0.0.1"}}, nil
}
func (f *fakeSandbox) Get(ctx context.Context, n string) (*sandbox.Instance, error) {
	return &sandbox.Instance{Name: n, Status: sandbox.StatusRunning, Addresses: []string{"10.0.0.1"}}, nil
}
func (f *fakeSandbox) List(ctx context.Context) ([]sandbox.Instance, error) { return nil, nil }
func (f *fakeSandbox) Start(ctx context.Context, n string) error            { f.started = append(f.started, n); return nil }
func (f *fakeSandbox) Stop(ctx context.Context, n string) error             { f.stopped = append(f.stopped, n); return nil }
func (f *fakeSandbox) Delete(ctx context.Context, n string) error           { f.deleted = append(f.deleted, n); return nil }
func (f *fakeSandbox) CreateSnapshot(ctx context.Context, n, l string) error { return nil }
func (f *fakeSandbox) ListSnapshots(ctx context.Context, n string) ([]sandbox.Snapshot, error) {
	return nil, nil
}
func (f *fakeSandbox) DeleteSnapshot(ctx context.Context, n, l string) error    { return nil }
func (f *fakeSandbox) RestoreSnapshot(ctx context.Context, n, l string) error   { return nil }
func (f *fakeSandbox) CloneFrom(ctx context.Context, src, lbl, nn string) error { return nil }
func (f *fakeSandbox) Run(ctx context.Context, n string, o sandbox.ExecOpts) (int, error) {
	return 0, nil
}
func (f *fakeSandbox) Output(ctx context.Context, n string, c []string) ([]byte, error) {
	return nil, nil
}
func (f *fakeSandbox) Console(ctx context.Context, n string, o sandbox.ConsoleOpts) error { return nil }
func (f *fakeSandbox) Ready(ctx context.Context, n string, t time.Duration) error         { return nil }
func (f *fakeSandbox) SetEgressMode(ctx context.Context, n string, m sandbox.EgressMode) error {
	return nil
}
func (f *fakeSandbox) AllowDomain(ctx context.Context, n, d string) error { return nil }
func (f *fakeSandbox) DenyDomain(ctx context.Context, n, d string) error  { return nil }
func (f *fakeSandbox) GetPolicy(ctx context.Context, n string) (*sandbox.Policy, error) {
	return nil, nil
}
func (f *fakeSandbox) Capabilities() sandbox.Capabilities { return sandbox.Capabilities{} }
func (f *fakeSandbox) Close() error                       { return nil }

// Files (in-memory implementations so EditFile etc. round-trip).
func (f *fakeSandbox) WriteFile(ctx context.Context, name, path string, content []byte, mode any) error {
	cp := make([]byte, len(content))
	copy(cp, content)
	f.files[path] = cp
	return nil
}
func (f *fakeSandbox) ReadFile(ctx context.Context, name, path string, maxBytes int64) ([]byte, bool, error) {
	b, ok := f.files[path]
	if !ok {
		return nil, false, &fileNotFound{path}
	}
	if maxBytes > 0 && int64(len(b)) > maxBytes {
		return b[:maxBytes], true, nil
	}
	return b, false, nil
}
func (f *fakeSandbox) ListFiles(ctx context.Context, name, path string, recursive bool) ([]sandbox.FileEntry, error) {
	return nil, nil
}
func (f *fakeSandbox) DeleteFile(ctx context.Context, name, path string) error {
	delete(f.files, path)
	return nil
}

type fileNotFound struct{ path string }

func (e *fileNotFound) Error() string { return "no such file: " + e.path }

// Note: WriteFile's `mode` arg uses `any` to avoid pulling os.FileMode into
// the test fake — adjust to os.FileMode if your linter complains.
```

> **Engineer note:** if your fake's signature mismatches the interface, change `mode any` to `os.FileMode` and add `"os"` to imports. The tests below depend on the in-memory `files` map.

```go
func newTestTools(t *testing.T) (*Tools, *fakeSandbox) {
	t.Helper()
	dir := t.TempDir()
	s, _ := LoadState(filepath.Join(dir, "s.json"))
	be := newFakeSandbox()
	return &Tools{
		State:          s,
		Backend:        be,
		Prefix:         "px-mcp-",
		DefaultImage:   "ubuntu/24.04",
		ExecTimeoutMax: 10 * time.Minute,
	}, be
}

func TestCreateSandboxAddsState(t *testing.T) {
	tt, be := newTestTools(t)
	out, err := tt.CreateSandbox(context.Background(), CreateSandboxIn{Label: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Name == "" || out.Status != "running" {
		t.Errorf("unexpected out: %+v", out)
	}
	if len(be.created) != 1 || be.created[0].Image != "ubuntu/24.04" {
		t.Errorf("unexpected create call: %+v", be.created)
	}
	if _, ok := tt.State.Get(out.Name); !ok {
		t.Error("sandbox not added to state")
	}
}

func TestDestroyRemovesState(t *testing.T) {
	tt, _ := newTestTools(t)
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})
	if _, err := tt.DestroySandbox(context.Background(), SandboxRef{Name: out.Name}); err != nil {
		t.Fatal(err)
	}
	if _, ok := tt.State.Get(out.Name); ok {
		t.Error("sandbox should be removed from state")
	}
}

func TestEditFileReplacesUnique(t *testing.T) {
	tt, be := newTestTools(t)
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})
	be.files["/app/main.py"] = []byte("print('hi')\n")

	res, err := tt.EditFile(context.Background(), EditFileIn{
		Name:      out.Name,
		Path:      "/app/main.py",
		OldString: "hi",
		NewString: "hello",
	})
	if err != nil {
		t.Fatalf("EditFile: %v", err)
	}
	if res.Replacements != 1 {
		t.Errorf("Replacements = %d, want 1", res.Replacements)
	}
	if got := string(be.files["/app/main.py"]); got != "print('hello')\n" {
		t.Errorf("file content = %q", got)
	}
}

func TestEditFileMissingErrors(t *testing.T) {
	tt, be := newTestTools(t)
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})
	be.files["/x"] = []byte("abc")
	_, err := tt.EditFile(context.Background(), EditFileIn{
		Name:      out.Name,
		Path:      "/x",
		OldString: "z",
		NewString: "y",
	})
	if err == nil {
		t.Fatal("expected error for missing old_string")
	}
}

func TestEditFileNonUniqueErrors(t *testing.T) {
	tt, be := newTestTools(t)
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})
	be.files["/x"] = []byte("aaa")
	_, err := tt.EditFile(context.Background(), EditFileIn{
		Name:      out.Name,
		Path:      "/x",
		OldString: "a",
		NewString: "b",
	})
	if err == nil {
		t.Fatal("expected error for non-unique old_string without replace_all")
	}
}

func TestEditFileReplaceAll(t *testing.T) {
	tt, be := newTestTools(t)
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})
	be.files["/x"] = []byte("aaa")
	res, err := tt.EditFile(context.Background(), EditFileIn{
		Name:       out.Name,
		Path:       "/x",
		OldString:  "a",
		NewString:  "b",
		ReplaceAll: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Replacements != 3 {
		t.Errorf("Replacements = %d, want 3", res.Replacements)
	}
	if string(be.files["/x"]) != "bbb" {
		t.Errorf("content = %q", be.files["/x"])
	}
}

func TestDeleteFileRemoves(t *testing.T) {
	tt, be := newTestTools(t)
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})
	be.files["/x"] = []byte("data")
	if _, err := tt.DeleteFile(context.Background(), DeleteFileIn{Name: out.Name, Path: "/x"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := be.files["/x"]; ok {
		t.Error("file should be deleted")
	}
}
```

**Step 3: Run + commit**

```
go test ./internal/mcp -v
go build ./...
```

Expected: PASS, build clean.

```bash
git add internal/mcp/tools.go internal/mcp/tools_test.go
jj commit -m "feat(mcp): add tool layer with lifecycle, exec, and file CRUD+edit"
```

---

## Task 12: Wire up the streamable-HTTP server

**Files:**
- Create: `internal/mcp/server.go`
- Create: `internal/mcp/server_test.go`
- Modify: `go.mod`, `go.sum`

**Verify SDK API first:**

```
go get github.com/modelcontextprotocol/go-sdk@latest
go doc github.com/modelcontextprotocol/go-sdk/mcp NewServer
go doc github.com/modelcontextprotocol/go-sdk/mcp AddTool
go doc github.com/modelcontextprotocol/go-sdk/mcp StreamableHTTPHandler
```

The current shape (early 2026) is roughly:

```go
import sdk "github.com/modelcontextprotocol/go-sdk/mcp"

srv := sdk.NewServer(&sdk.Implementation{Name: "pixels-mcp", Version: "0.1.0"}, nil)
sdk.AddTool(srv, &sdk.Tool{Name: "create_sandbox", Description: "..."}, tools.CreateSandbox)
handler := sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server { return srv }, nil)
http.Handle(endpointPath, handler)
```

If `AddTool` requires a different handler signature (e.g. `func(ctx, *CallToolRequest, In) (*CallToolResult, Out, error)`), wrap each `Tools.Foo` in a small adapter that forwards arguments and constructs the SDK response wrapper.

**Step 1: Implement `internal/mcp/server.go`**

```go
package mcp

import (
	"context"
	"net/http"
	"time"

	"github.com/deevus/pixels/sandbox"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerOpts bundles construction parameters.
type ServerOpts struct {
	State          *State
	Backend        sandbox.Sandbox
	Prefix         string
	DefaultImage   string
	ExecTimeoutMax time.Duration
}

// NewServer wires the MCP tool surface and returns an HTTP handler ready to mount.
func NewServer(opts ServerOpts, endpointPath string) (http.Handler, *Tools) {
	tools := &Tools{
		State:          opts.State,
		Backend:        opts.Backend,
		Prefix:         opts.Prefix,
		DefaultImage:   opts.DefaultImage,
		ExecTimeoutMax: opts.ExecTimeoutMax,
	}

	srv := sdk.NewServer(&sdk.Implementation{Name: "pixels-mcp", Version: "0.1.0"}, nil)

	sdk.AddTool(srv, &sdk.Tool{Name: "create_sandbox", Description: "Create a new ephemeral sandbox container."}, tools.CreateSandbox)
	sdk.AddTool(srv, &sdk.Tool{Name: "destroy_sandbox", Description: "Destroy a sandbox and its filesystem."}, tools.DestroySandbox)
	sdk.AddTool(srv, &sdk.Tool{Name: "start_sandbox", Description: "Start (resume) a stopped sandbox."}, tools.StartSandbox)
	sdk.AddTool(srv, &sdk.Tool{Name: "stop_sandbox", Description: "Stop (pause) a running sandbox."}, tools.StopSandbox)
	sdk.AddTool(srv, &sdk.Tool{Name: "list_sandboxes", Description: "List all tracked sandboxes."}, tools.ListSandboxes)
	sdk.AddTool(srv, &sdk.Tool{Name: "exec", Description: "Run a command inside a sandbox."}, tools.Exec)
	sdk.AddTool(srv, &sdk.Tool{Name: "write_file", Description: "Write a file inside a sandbox (create or full overwrite)."}, tools.WriteFile)
	sdk.AddTool(srv, &sdk.Tool{Name: "read_file", Description: "Read a file from a sandbox, optionally truncated."}, tools.ReadFile)
	sdk.AddTool(srv, &sdk.Tool{Name: "list_files", Description: "List files inside a sandbox path."}, tools.ListFiles)
	sdk.AddTool(srv, &sdk.Tool{Name: "edit_file", Description: "Replace one occurrence of old_string with new_string in a file. Pass replace_all=true to replace every occurrence."}, tools.EditFile)
	sdk.AddTool(srv, &sdk.Tool{Name: "delete_file", Description: "Delete a single file from a sandbox."}, tools.DeleteFile)

	handler := sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server { return srv }, nil)
	mux := http.NewServeMux()
	mux.Handle(endpointPath, handler)
	return mux, tools
}

// _ assigns ctx so it isn't reported unused if a future SDK adapter drops it.
var _ context.Context = nil
```

> **Engineer note:** if `AddTool` rejects the `func(ctx, In) (Out, error)` shape directly, wrap each handler in a thin adapter:
>
> ```go
> func adapt[I, O any](fn func(context.Context, I) (O, error)) func(ctx context.Context, req *sdk.CallToolRequest, in I) (*sdk.CallToolResult, O, error) {
>     return func(ctx context.Context, _ *sdk.CallToolRequest, in I) (*sdk.CallToolResult, O, error) {
>         out, err := fn(ctx, in)
>         return nil, out, err
>     }
> }
> ```
>
> ...and pass `adapt(tools.CreateSandbox)` etc. into AddTool.

**Step 2: Write a smoke test**

```go
package mcp

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestNewServerMountsHandler(t *testing.T) {
	dir := t.TempDir()
	st, _ := LoadState(filepath.Join(dir, "s.json"))
	be := newFakeSandbox()

	mux, _ := NewServer(ServerOpts{
		State:          st,
		Backend:        be,
		Prefix:         "px-mcp-",
		ExecTimeoutMax: 10 * time.Minute,
	}, "/mcp")

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/mcp")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		t.Errorf("unexpected 5xx: %d", resp.StatusCode)
	}
}
```

**Step 3: Run + commit**

```
go mod tidy
go test ./internal/mcp -v
go build ./...
```

Expected: PASS.

```bash
git add internal/mcp/server.go internal/mcp/server_test.go go.mod go.sum
jj commit -m "feat(mcp): wire streamable-HTTP server with all tool registrations"
```

---

## Task 13: Add `pixels mcp` cobra subcommand

**Files:**
- Create: `cmd/mcp.go`

**Step 1: Implement the command**

```go
package cmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/deevus/pixels/internal/config"
	mcppkg "github.com/deevus/pixels/internal/mcp"
	"github.com/spf13/cobra"
)

var (
	mcpListenAddr string
	mcpStateFile  string
	mcpPIDFile    string
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run the pixels MCP server (streamable-HTTP)",
	RunE:  runMCP,
}

func init() {
	mcpCmd.Flags().StringVar(&mcpListenAddr, "listen-addr", "", "override [mcp].listen_addr")
	mcpCmd.Flags().StringVar(&mcpStateFile, "state-file", "", "override [mcp].state_file")
	mcpCmd.Flags().StringVar(&mcpPIDFile, "pid-file", "", "override [mcp].pid_file")
	rootCmd.AddCommand(mcpCmd)
}

func runMCP(cmd *cobra.Command, args []string) error {
	cfg, ok := cmd.Context().Value(configKey).(*config.Config)
	if !ok {
		return fmt.Errorf("config not loaded")
	}

	listenAddr := pickStr(mcpListenAddr, cfg.MCP.ListenAddr)
	stateFile := pickStr(mcpStateFile, cfg.MCPStateFile())
	pidFile := pickStr(mcpPIDFile, cfg.MCPPIDFile())

	pf, err := mcppkg.AcquirePIDFile(pidFile)
	if err != nil {
		return err
	}
	defer pf.Release()

	state, err := mcppkg.LoadState(stateFile)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	idle, err := time.ParseDuration(cfg.MCP.IdleStopAfter)
	if err != nil {
		return fmt.Errorf("idle_stop_after: %w", err)
	}
	hard, err := time.ParseDuration(cfg.MCP.HardDestroyAfter)
	if err != nil {
		return fmt.Errorf("hard_destroy_after: %w", err)
	}
	reapInterval, err := time.ParseDuration(cfg.MCP.ReapInterval)
	if err != nil {
		return fmt.Errorf("reap_interval: %w", err)
	}
	execMax, err := time.ParseDuration(cfg.MCP.ExecTimeoutMax)
	if err != nil {
		return fmt.Errorf("exec_timeout_max: %w", err)
	}

	defaultImg := cfg.MCP.DefaultImage
	if defaultImg == "" {
		defaultImg = cfg.Defaults.Image
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mux, _ := mcppkg.NewServer(mcppkg.ServerOpts{
		State:          state,
		Backend:        sb,
		Prefix:         cfg.MCP.Prefix,
		DefaultImage:   defaultImg,
		ExecTimeoutMax: execMax,
	}, cfg.MCP.EndpointPath)

	reaper := &mcppkg.Reaper{
		State:            state,
		Backend:          sb,
		IdleStopAfter:    idle,
		HardDestroyAfter: hard,
	}
	reaper.Tick(ctx) // immediate startup pass
	go reaper.Run(ctx, reapInterval)

	srv := &http.Server{Addr: listenAddr, Handler: mux}

	if !isLoopback(listenAddr) {
		fmt.Fprintf(os.Stderr, "pixels mcp: WARNING bound non-loopback address %q with no auth\n", listenAddr)
	}

	go func() {
		fmt.Fprintf(os.Stderr, "pixels mcp: listening on http://%s%s\n", listenAddr, cfg.MCP.EndpointPath)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "pixels mcp: listen: %v\n", err)
			cancel()
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-stop:
	case <-ctx.Done():
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	cancel()
	_ = state.Save()
	return nil
}

func pickStr(override, fallback string) string {
	if override != "" {
		return override
	}
	return fallback
}

func isLoopback(addr string) bool {
	return strings.HasPrefix(addr, "127.") || strings.HasPrefix(addr, "localhost")
}
```

> **Engineer note:** the `configKey` reference must match how `cmd/root.go` propagates config. Look at the `PersistentPreRunE` in `cmd/root.go` and reuse the same context key. If the existing pattern instead reads a package-level variable (e.g. `cfg`), drop the context lookup and use that instead.

**Step 2: Build and confirm**

```
go build -o pixels .
./pixels mcp --help
```

Expected: help text with the three flags.

**Step 3: Run tests**

```
go test ./... -v
```

Expected: PASS.

**Step 4: Commit**

```bash
git add cmd/mcp.go
jj commit -m "feat(cmd): add 'pixels mcp' subcommand"
```

---

## Task 14: README documentation

**Files:**
- Modify: `README.md`

**Step 1: Append a section**

```markdown
## Using `pixels` as an MCP code-sandbox server

`pixels mcp` runs a streamable-HTTP MCP server that exposes container
lifecycle, exec, and file CRUD as MCP tools. Run it once on your
machine, then point any number of MCP clients at it.

### Start the daemon

    pixels mcp

By default it binds to `http://127.0.0.1:8765/mcp` and refuses to start
if another instance is already running (PID file at
`~/.cache/pixels/mcp.pid`).

### Configure your client

Claude Code MCP entry:

    {
      "mcpServers": {
        "pixels": { "url": "http://127.0.0.1:8765/mcp" }
      }
    }

### Tools

| Tool | What it does |
|---|---|
| `create_sandbox` | Spin up a new ephemeral container |
| `list_sandboxes` | List tracked sandboxes |
| `start_sandbox` / `stop_sandbox` / `destroy_sandbox` | Lifecycle |
| `exec` | Run a command inside a sandbox |
| `write_file` | Create or fully overwrite a file |
| `read_file` | Read a file (optional truncation via `max_bytes`) |
| `edit_file` | Replace `old_string` with `new_string` (with optional `replace_all`) |
| `delete_file` | Remove a file |
| `list_files` | List directory contents (optionally recursive) |

### Lifetimes

Two TTLs apply (configurable in `[mcp]` config):

- `idle_stop_after` (default 1h) — running sandbox with no recent
  activity gets stopped.
- `hard_destroy_after` (default 24h) — any sandbox older than this is
  destroyed and removed from state.
```

**Step 2: Manual smoke test**

In one terminal:

```
pixels mcp --listen-addr 127.0.0.1:8765
```

In another:

```
curl -i http://127.0.0.1:8765/mcp
```

Expected: a valid HTTP response (the SDK will likely 4xx a non-MCP request, which is fine — we just want "server is up").

Real client flow: point Claude Code at the URL and from a chat:

1. `create_sandbox` — confirm container shows up in `pixels list`.
2. `write_file` → `read_file` round-trips byte-for-byte.
3. `edit_file` with a unique substring updates the file in place.
4. `edit_file` with a non-unique substring without `replace_all` returns an error.
5. `delete_file` removes the file (verify with `list_files`).
6. `exec` runs a command and returns stdout/stderr/exit code.
7. `destroy_sandbox` removes the container from `pixels list`.

**Step 3: Commit**

```bash
git add README.md
jj commit -m "docs: document 'pixels mcp' server usage and tool surface"
```

---

## Task 15: Final cleanup

**Step 1: Run the full test + build matrix**

```
go test ./... -race
go build ./...
go vet ./...
```

All three should pass clean.

**Step 2: Confirm no leftover TODOs**

```
grep -rn "TODO\|FIXME\|panic(\"not implemented\")" internal/mcp sandbox/filesexec.go cmd/mcp.go
```

Expected: no output (or only intentional TODOs you want to keep).

**Step 3: Final commit only if anything changed**

If the cleanup pass produced edits:

```bash
jj commit -m "chore(mcp): final cleanup"
```

Otherwise nothing to do.

---

## Out of scope for v1

These are explicitly *not* in this plan and should be left alone:

- Auth tokens / TLS — loopback-only is the trust boundary.
- Native Incus file push/pull — `FilesViaExec` is fine for v1; swap in later if performance matters.
- MultiEdit (batched edits in one tool call).
- `mkdir`, `rename`, `move` tools — agents use `exec` for these.
- Recursive directory delete — agents use `exec rm -rf` for these.
- Checkpoint/restore as MCP tools.
- Network-policy MCP tools.
- A `pixels mcp doctor` subcommand.
- Streaming `read_file_chunk(offset, length)`.

If you find yourself wanting one of these, stop and check with the user first.
