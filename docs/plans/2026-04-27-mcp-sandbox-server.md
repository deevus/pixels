# MCP Sandbox Server Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a `pixels mcp` subcommand that runs a streamable-HTTP MCP server, exposing pixels container lifecycle (create/start/stop/destroy) plus exec and SFTP file I/O as MCP tools, so AI agents can drive disposable Linux sandboxes.

**Architecture:** Long-running daemon, single instance per host (pidfile-gated), bound to `127.0.0.1` by default. Tool handlers call into the existing `sandbox.Sandbox` interface for lifecycle and into a new programmatic SSH/SFTP client for file ops. State persisted to a JSON file with atomic writes; an in-memory `sync.RWMutex` guards reads/writes; a reaper goroutine enforces idle-stop and hard-destroy TTLs. See `docs/plans/2026-04-27-mcp-sandbox-server-design.md` for the design write-up.

**Tech Stack:**
- Go 1.25
- `github.com/modelcontextprotocol/go-sdk` — official MCP Go SDK (streamable-HTTP server)
- `golang.org/x/crypto/ssh` — programmatic SSH client (already an indirect dep)
- `github.com/pkg/sftp` — SFTP over SSH (already an indirect dep)
- `github.com/BurntSushi/toml` + `github.com/caarlos0/env/v11` — existing config pattern
- `github.com/spf13/cobra` — existing CLI framework
- Existing packages: `internal/config`, `internal/ssh`, `sandbox`, `sandbox/truenas`, `sandbox/incus`

---

## Engineer-orientation notes

Before starting:

1. **Read the design doc** at `docs/plans/2026-04-27-mcp-sandbox-server-design.md`. Every architectural decision in this plan is justified there.
2. **Read `sandbox/sandbox.go`** to understand the `Sandbox` interface and `Instance`/`CreateOpts`/`ExecOpts` types. The MCP tool handlers call into this interface, not into backend-specific code.
3. **Read `internal/config/config.go`** to understand the config-loading pattern (TOML defaults → file → env-var overrides via `caarlos0/env`).
4. **Read `cmd/root.go`** to understand how cobra commands access config and open a sandbox via `openSandbox()`.
5. The existing `internal/ssh` package shells out to the `ssh` CLI. We are intentionally **not** modifying it. The new SSH/SFTP client lives in a separate file (`internal/sshclient/sshclient.go`) so MCP file ops don't disturb existing CLI behavior.
6. **Verify the MCP Go SDK API** before Task 9. The SDK is young; check `pkg.go.dev/github.com/modelcontextprotocol/go-sdk` for the current `mcp.NewServer` / `AddTool` / `StreamableHTTPHandler` shape. If the API has shifted, adapt — the data flow stays the same.

**Commit cadence:** one commit per task. Each task is small enough that the commit message can be a single line.

**Test conventions:** table-driven where possible (the codebase uses this pattern — see `cmd/resolve_test.go`, `internal/ssh/ssh_test.go`). Mock-based tests for backend-dependent code.

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

**Step 2: Run the tests to verify failure**

```
go test ./internal/config -run TestMCP -v
```

Expected: FAIL — `cfg.MCP` undefined.

**Step 3: Add the `MCP` struct and defaults**

In `internal/config/config.go`, add the `MCP` field to `Config` and a new struct:

```go
// add to Config struct
MCP        MCP            `toml:"mcp"`

// new struct (place near other backend-config structs)
type MCP struct {
	Prefix           string `toml:"prefix"            env:"PIXELS_MCP_PREFIX"`
	DefaultImage     string `toml:"default_image"     env:"PIXELS_MCP_DEFAULT_IMAGE"`
	IdleStopAfter    string `toml:"idle_stop_after"   env:"PIXELS_MCP_IDLE_STOP_AFTER"`
	HardDestroyAfter string `toml:"hard_destroy_after" env:"PIXELS_MCP_HARD_DESTROY_AFTER"`
	ReapInterval     string `toml:"reap_interval"     env:"PIXELS_MCP_REAP_INTERVAL"`
	StateFile        string `toml:"state_file"        env:"PIXELS_MCP_STATE_FILE"`
	PIDFile          string `toml:"pid_file"          env:"PIXELS_MCP_PID_FILE"`
	ExecTimeoutMax   string `toml:"exec_timeout_max"  env:"PIXELS_MCP_EXEC_TIMEOUT_MAX"`
	ListenAddr       string `toml:"listen_addr"       env:"PIXELS_MCP_LISTEN_ADDR"`
	EndpointPath     string `toml:"endpoint_path"     env:"PIXELS_MCP_ENDPOINT_PATH"`
}
```

In `Load()`, set defaults inside the initial `cfg := &Config{ ... }` block:

```go
MCP: MCP{
	Prefix:           "px-mcp-",
	DefaultImage:     "",            // resolved later from cfg.Defaults.Image if empty
	IdleStopAfter:    "1h",
	HardDestroyAfter: "24h",
	ReapInterval:     "1m",
	ExecTimeoutMax:   "10m",
	ListenAddr:       "127.0.0.1:8765",
	EndpointPath:     "/mcp",
	// StateFile, PIDFile resolved from XDG cache dir at use time, not here
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
git commit -m "feat(config): add [mcp] config section with defaults"
```

---

## Task 2: Add MCP cache-path helpers

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Why:** State file and pidfile default to `~/.cache/pixels/`. Centralize the path resolution so the MCP package doesn't reach into config internals.

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

**Step 2: Run to verify failure**

```
go test ./internal/config -run TestMCP.*Path -v
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
git commit -m "feat(config): add MCPStateFile/MCPPIDFile path helpers"
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

**Step 2: Run to verify failure**

```
go test ./internal/mcp -v
```

Expected: FAIL — package or types undefined.

**Step 3: Implement the state types**

Create `internal/mcp/state.go`:

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
		// Corrupt state — log and start clean. Orphan sandboxes will need
		// manual cleanup via `pixels list` / `pixels destroy`.
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

**Step 4: Run tests**

```
go test ./internal/mcp -v
```

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/mcp/
git commit -m "feat(mcp): add State type with in-memory + JSON persistence"
```

---

## Task 4: Add atomic-write and corruption tests for State.Save

**Files:**
- Modify: `internal/mcp/state_test.go`

**Step 1: Write the failing tests**

Append:

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

**Step 2: Run**

```
go test ./internal/mcp -run TestStateSave -v
go test ./internal/mcp -run TestStateLoadCorrupt -v
```

Expected: PASS (these exercise behavior already implemented in Task 3).

**Step 3: Commit**

```bash
git add internal/mcp/state_test.go
git commit -m "test(mcp): cover atomic save and corrupt-state recovery"
```

---

## Task 5: Implement pidfile (single-instance lock)

**Files:**
- Create: `internal/mcp/pidfile.go`
- Create: `internal/mcp/pidfile_test.go`

**Step 1: Write the failing test**

Create `internal/mcp/pidfile_test.go`:

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
	// Write our own PID (definitely alive) to simulate another live daemon.
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
	// PID 1 is init/launchd — alive — so use a PID guaranteed not to exist.
	// Picking a high PID that almost certainly is unused.
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

**Step 2: Run**

```
go test ./internal/mcp -run TestPIDFile -v
go test ./internal/mcp -run TestAcquirePIDFile -v
```

Expected: FAIL — `AcquirePIDFile` undefined.

**Step 3: Implement**

Create `internal/mcp/pidfile.go`:

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
		// stale — fall through and overwrite
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
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}
```

**Step 4: Run**

```
go test ./internal/mcp -run TestPIDFile -v
go test ./internal/mcp -run TestAcquirePIDFile -v
```

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/mcp/
git commit -m "feat(mcp): add pidfile with stale-PID detection"
```

---

## Task 6: Add programmatic SSH/SFTP client

**Files:**
- Create: `internal/sshclient/sshclient.go`
- Create: `internal/sshclient/sshclient_test.go`

**Why a separate package:** The existing `internal/ssh` shells out to the `ssh` CLI. SFTP requires a programmatic SSH client. Keeping these in separate packages avoids changing CLI behavior.

**Step 1: Write the failing test**

The test uses an in-memory SSH server from `golang.org/x/crypto/ssh` so we don't need a real machine.

Create `internal/sshclient/sshclient_test.go`:

```go
package sshclient

import (
	"context"
	"net"
	"testing"
	"time"
)

// minimal smoke test that the constructor wires options correctly.
// Full SFTP behavior is exercised in mcp/files_test.go using a fixture server.
func TestNewClientDialFailure(t *testing.T) {
	// Use a port nothing listens on. Connection refused = construction reaches dial.
	_, err := NewClient(context.Background(), Config{
		Host:    "127.0.0.1:1",
		User:    "test",
		Timeout: 200 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected dial error")
	}
}

func TestConfigAddrDefault(t *testing.T) {
	cfg := Config{Host: "1.2.3.4"}
	got := cfg.addr()
	if _, _, err := net.SplitHostPort(got); err != nil {
		t.Errorf("addr should be host:port, got %q (%v)", got, err)
	}
}
```

**Step 2: Run to verify failure**

```
go test ./internal/sshclient -v
```

Expected: FAIL — package undefined.

**Step 3: Implement**

Create `internal/sshclient/sshclient.go`:

```go
// Package sshclient provides a programmatic SSH and SFTP client used by the
// MCP server for file I/O into sandboxes. It is intentionally separate from
// internal/ssh, which shells out to the ssh CLI for interactive sessions.
package sshclient

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Config holds connection parameters.
type Config struct {
	Host           string        // host or host:port
	User           string
	KeyPath        string        // path to private key
	KnownHostsPath string        // path to known_hosts; empty = InsecureIgnoreHostKey
	Timeout        time.Duration // dial timeout; default 10s if zero
}

func (c Config) addr() string {
	if strings.Contains(c.Host, ":") {
		return c.Host
	}
	return c.Host + ":22"
}

// Client wraps an SSH client and exposes Exec and SFTP.
type Client struct {
	ssh *ssh.Client
}

// NewClient dials and authenticates using the configured private key.
func NewClient(ctx context.Context, cfg Config) (*Client, error) {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	keyBytes, err := os.ReadFile(cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("read key %q: %w", cfg.KeyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("parse key: %w", err)
	}

	hostKeyCallback := ssh.InsecureIgnoreHostKey()
	if cfg.KnownHostsPath != "" {
		cb, err := knownhosts.New(cfg.KnownHostsPath)
		if err != nil {
			return nil, fmt.Errorf("load known_hosts: %w", err)
		}
		hostKeyCallback = cb
	}

	sshCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKeyCallback,
		Timeout:         timeout,
	}

	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", cfg.addr())
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", cfg.addr(), err)
	}

	c, chans, reqs, err := ssh.NewClientConn(conn, cfg.addr(), sshCfg)
	if err != nil {
		return nil, fmt.Errorf("ssh handshake: %w", err)
	}
	return &Client{ssh: ssh.NewClient(c, chans, reqs)}, nil
}

// Close releases the underlying SSH connection.
func (c *Client) Close() error { return c.ssh.Close() }

// ExecResult is the captured output of a single command run.
type ExecResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// Exec runs a command on the remote host and returns its captured output.
func (c *Client) Exec(ctx context.Context, command []string, env map[string]string, cwd string) (*ExecResult, error) {
	sess, err := c.ssh.NewSession()
	if err != nil {
		return nil, fmt.Errorf("new session: %w", err)
	}
	defer sess.Close()

	for k, v := range env {
		// Best-effort. Many sshd configs reject SetEnv for unlisted vars.
		_ = sess.Setenv(k, v)
	}

	cmd := buildShellCommand(command, cwd)
	go func() { <-ctx.Done(); _ = sess.Signal(ssh.SIGKILL) }()

	stdoutC := make(chan []byte, 1)
	stderrC := make(chan []byte, 1)

	stdout, _ := sess.StdoutPipe()
	stderr, _ := sess.StderrPipe()
	if err := sess.Start(cmd); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	go func() {
		b, _ := readAll(stdout)
		stdoutC <- b
	}()
	go func() {
		b, _ := readAll(stderr)
		stderrC <- b
	}()

	exitErr := sess.Wait()
	res := &ExecResult{
		Stdout: <-stdoutC,
		Stderr: <-stderrC,
	}
	if exitErr == nil {
		res.ExitCode = 0
	} else if ee, ok := exitErr.(*ssh.ExitError); ok {
		res.ExitCode = ee.ExitStatus()
	} else {
		// e.g. signal kill from ctx cancel
		res.ExitCode = -1
		return res, exitErr
	}
	return res, nil
}

// SFTP returns a fresh SFTP subsystem session. Caller must Close it.
func (c *Client) SFTP() (*sftp.Client, error) { return sftp.NewClient(c.ssh) }
```

Add a tiny helper file `internal/sshclient/util.go`:

```go
package sshclient

import (
	"io"

	shellescape "al.essio.dev/pkg/shellescape"
)

func readAll(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	return io.ReadAll(r)
}

// buildShellCommand wraps argv in `cd && exec` when cwd is set, properly quoted.
func buildShellCommand(command []string, cwd string) string {
	cmd := shellescape.QuoteCommand(command)
	if cwd == "" {
		return cmd
	}
	return "cd " + shellescape.Quote(cwd) + " && " + cmd
}
```

**Step 4: Update `go.mod` and run**

```
go get golang.org/x/crypto/ssh golang.org/x/crypto/ssh/knownhosts github.com/pkg/sftp
go mod tidy
go test ./internal/sshclient -v
go build ./...
```

Expected: PASS, build clean.

**Step 5: Commit**

```bash
git add internal/sshclient/ go.mod go.sum
git commit -m "feat(sshclient): add programmatic SSH/SFTP client for MCP file ops"
```

---

## Task 7: Implement SFTP file ops in `internal/mcp/files.go`

**Files:**
- Create: `internal/mcp/files.go`
- Create: `internal/mcp/files_test.go`

**Step 1: Write the failing test (using a real SFTP roundtrip via ssh test server)**

Setting up an in-process SSH server for the test is verbose but tractable. Use the helper from `golang.org/x/crypto/ssh` and `pkg/sftp`'s `NewServer`. This test fixture is reusable across the file_test cases.

Create `internal/mcp/files_test.go`:

```go
package mcp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deevus/pixels/internal/sshclient"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// startTestSSHServer launches an in-process SSH+SFTP server listening on a
// random localhost port. It returns the addr, a path to a generated client
// private key, and a cleanup func. It serves the temp dir as the SFTP root.
func startTestSSHServer(t *testing.T, sftpRoot string) (addr, keyPath string, cleanup func()) {
	t.Helper()

	hostKeyPub, hostKeyPriv, err := ed25519.GenerateKey(rand.Reader)
	_ = hostKeyPub
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostKeyPriv)
	if err != nil {
		t.Fatal(err)
	}

	clientPub, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSSHPub, _ := ssh.NewPublicKey(clientPub)

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), clientSSHPub.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("bad key")
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSSH(nc, cfg, sftpRoot)
		}
	}()

	keyDir := t.TempDir()
	keyPath = filepath.Join(keyDir, "id_ed25519")
	pemBytes, err := ssh.MarshalPrivateKey(clientPriv, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	return ln.Addr().String(), keyPath, func() { _ = ln.Close() }
}

func serveSSH(nc net.Conn, cfg *ssh.ServerConfig, root string) {
	conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			newCh.Reject(ssh.UnknownChannelType, "")
			continue
		}
		ch, in, err := newCh.Accept()
		if err != nil {
			return
		}
		go func(in <-chan *ssh.Request) {
			for req := range in {
				switch req.Type {
				case "subsystem":
					if string(req.Payload[4:]) == "sftp" {
						req.Reply(true, nil)
						srv, _ := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(root))
						_ = srv.Serve()
						_ = ch.Close()
						return
					}
					req.Reply(false, nil)
				case "exec":
					req.Reply(true, nil)
					_, _ = io.Copy(io.Discard, ch)
					ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
					ch.Close()
					return
				default:
					req.Reply(false, nil)
				}
			}
		}(in)
	}
}

// connect helper
func dial(t *testing.T, addr, keyPath string) *sshclient.Client {
	t.Helper()
	c, err := sshclient.NewClient(context.Background(), sshclient.Config{
		Host:    addr,
		User:    "anyuser",
		KeyPath: keyPath,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestWriteFileAndReadFile(t *testing.T) {
	root := t.TempDir()
	addr, keyPath, cleanup := startTestSSHServer(t, root)
	defer cleanup()

	c := dial(t, addr, keyPath)

	if err := WriteFile(c, "hello.txt", []byte("hi there"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, truncated, err := ReadFile(c, "hello.txt", 0)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if truncated {
		t.Error("truncated should be false")
	}
	if string(got) != "hi there" {
		t.Errorf("got %q, want %q", got, "hi there")
	}
}

func TestReadFileMaxBytesTruncates(t *testing.T) {
	root := t.TempDir()
	addr, keyPath, cleanup := startTestSSHServer(t, root)
	defer cleanup()

	c := dial(t, addr, keyPath)
	if err := WriteFile(c, "big.txt", []byte("0123456789"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, truncated, err := ReadFile(c, "big.txt", 4)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !truncated {
		t.Error("expected truncated=true")
	}
	if string(got) != "0123" {
		t.Errorf("got %q, want %q", got, "0123")
	}
}

func TestListFiles(t *testing.T) {
	root := t.TempDir()
	addr, keyPath, cleanup := startTestSSHServer(t, root)
	defer cleanup()

	c := dial(t, addr, keyPath)
	if err := WriteFile(c, "a.txt", []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(c, "b.txt", []byte("bb"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := ListFiles(c, ".", false)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if got := len(entries); got != 2 {
		t.Fatalf("entries = %d, want 2: %+v", got, entries)
	}
}
```

**Step 2: Run to verify failure**

```
go test ./internal/mcp -run "TestWriteFile|TestReadFile|TestListFiles" -v
```

Expected: FAIL — `WriteFile`/`ReadFile`/`ListFiles` undefined.

**Step 3: Implement `internal/mcp/files.go`**

```go
package mcp

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/deevus/pixels/internal/sshclient"
)

// FileEntry is one row in a list_files result.
type FileEntry struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Mode  string `json:"mode"`
	IsDir bool   `json:"is_dir"`
}

// WriteFile uploads content to path on the remote host with the given mode.
// Creates parent directories as needed.
func WriteFile(c *sshclient.Client, path string, content []byte, mode os.FileMode) error {
	sftp, err := c.SFTP()
	if err != nil {
		return fmt.Errorf("sftp: %w", err)
	}
	defer sftp.Close()

	if dir := filepath.Dir(path); dir != "." && dir != "/" {
		if err := sftp.MkdirAll(dir); err != nil {
			return fmt.Errorf("mkdirall %s: %w", dir, err)
		}
	}

	f, err := sftp.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	if _, err := f.Write(content); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := sftp.Chmod(path, mode); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

// ReadFile downloads up to maxBytes bytes from path. maxBytes <= 0 means no
// limit. Returns content, whether it was truncated, and error.
func ReadFile(c *sshclient.Client, path string, maxBytes int64) ([]byte, bool, error) {
	sftp, err := c.SFTP()
	if err != nil {
		return nil, false, fmt.Errorf("sftp: %w", err)
	}
	defer sftp.Close()

	f, err := sftp.Open(path)
	if err != nil {
		return nil, false, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	if maxBytes <= 0 {
		b, err := io.ReadAll(f)
		return b, false, err
	}

	buf := make([]byte, maxBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, false, err
	}

	// Read one more byte to detect truncation.
	one := make([]byte, 1)
	extra, _ := f.Read(one)
	truncated := extra > 0
	return buf[:n], truncated, nil
}

// ListFiles returns directory contents at path. If recursive, descends.
func ListFiles(c *sshclient.Client, path string, recursive bool) ([]FileEntry, error) {
	sftp, err := c.SFTP()
	if err != nil {
		return nil, err
	}
	defer sftp.Close()

	if !recursive {
		infos, err := sftp.ReadDir(path)
		if err != nil {
			return nil, err
		}
		out := make([]FileEntry, 0, len(infos))
		for _, fi := range infos {
			out = append(out, FileEntry{
				Path:  filepath.Join(path, fi.Name()),
				Size:  fi.Size(),
				Mode:  fi.Mode().String(),
				IsDir: fi.IsDir(),
			})
		}
		return out, nil
	}

	var out []FileEntry
	w := sftp.Walk(path)
	for w.Step() {
		if err := w.Err(); err != nil {
			continue
		}
		fi := w.Stat()
		if w.Path() == path {
			continue
		}
		out = append(out, FileEntry{
			Path:  w.Path(),
			Size:  fi.Size(),
			Mode:  fi.Mode().String(),
			IsDir: fi.IsDir(),
		})
	}
	return out, nil
}
```

**Step 4: Run**

```
go test ./internal/mcp -v
go build ./...
```

Expected: PASS, build clean.

**Step 5: Commit**

```bash
git add internal/mcp/files.go internal/mcp/files_test.go
git commit -m "feat(mcp): add SFTP-based write_file/read_file/list_files"
```

---

## Task 8: Implement reaper

**Files:**
- Create: `internal/mcp/reaper.go`
- Create: `internal/mcp/reaper_test.go`

**Step 1: Write the failing test**

Create `internal/mcp/reaper_test.go`:

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
	mu       sync.Mutex
	stopped  []string
	deleted  []string
	stopErr  error
	delErr   error
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
		LastActivityAt: now.Add(-90 * time.Minute), // > 1h idle
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
	if len(be.deleted) != 0 {
		t.Errorf("deleted = %v, want none", be.deleted)
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

**Step 2: Run to verify failure**

```
go test ./internal/mcp -run TestReaper -v
```

Expected: FAIL.

**Step 3: Implement**

Create `internal/mcp/reaper.go`:

```go
package mcp

import (
	"context"
	"fmt"
	"os"
	"time"
)

// LifecycleBackend is the subset of sandbox.Backend that the reaper needs.
// Defined locally so reaper tests don't need to satisfy the full Sandbox interface.
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

**Step 4: Run**

```
go test ./internal/mcp -run TestReaper -v
go build ./...
```

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/mcp/reaper.go internal/mcp/reaper_test.go
git commit -m "feat(mcp): add reaper with idle-stop and hard-destroy TTLs"
```

---

## Task 9: Add MCP Go SDK dependency and tool layer

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/mcp/tools.go`
- Create: `internal/mcp/tools_test.go`

**Verify SDK API first:**

```
go get github.com/modelcontextprotocol/go-sdk@latest
go doc github.com/modelcontextprotocol/go-sdk/mcp | head -80
```

Look for: `NewServer`, `AddTool`, `StreamableHTTPHandler`. The SDK signatures for `AddTool` may want a typed handler `func(ctx, *CallToolRequest, In) (*CallToolResult, Out, error)` or similar — adapt the Tool helpers below to the current signature.

**Step 1: Define the tool input/output structs**

Create `internal/mcp/tools.go`:

```go
package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/deevus/pixels/internal/sshclient"
	"github.com/deevus/pixels/sandbox"
)

// SandboxFactory builds an SSH client to a tracked sandbox. Injectable for tests.
type SandboxFactory func(ctx context.Context, sb Sandbox) (*sshclient.Client, error)

// Tools is the dependency bundle every MCP handler closes over.
type Tools struct {
	State    *State
	Backend  sandbox.Sandbox
	Connect  SandboxFactory
	Prefix   string
	DefaultImage string
	ExecTimeoutMax time.Duration

	// Per-sandbox mutex map. Tool handlers acquire the lock for the duration
	// of an SSH/SFTP call so concurrent ops on one sandbox don't race.
	mu       sync.Mutex
	sbLocks  map[string]*sync.Mutex
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
	Mode    string `json:"mode,omitempty"` // octal e.g. "0644"
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
	Entries []FileEntry `json:"entries"`
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

// --- Handlers (return values, no MCP-SDK types here so tests are simple) ---

func (t *Tools) CreateSandbox(ctx context.Context, in CreateSandboxIn) (CreateSandboxOut, error) {
	image := in.Image
	if image == "" {
		image = t.DefaultImage
	}
	name := t.generateName()
	inst, err := t.Backend.Create(ctx, sandbox.CreateOpts{
		Name:  name,
		Image: image,
	})
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

	c, err := t.Connect(ctx, sb)
	if err != nil {
		return ExecOut{}, err
	}
	defer c.Close()

	res, err := c.Exec(ctx, in.Command, in.Env, in.Cwd)
	if res == nil {
		res = &sshclient.ExecResult{}
	}
	t.State.BumpActivity(sb.Name, time.Now().UTC())
	_ = t.State.Save()
	out := ExecOut{
		ExitCode: res.ExitCode,
		Stdout:   string(res.Stdout),
		Stderr:   string(res.Stderr),
	}
	if err != nil && res.ExitCode == 0 {
		return out, err
	}
	return out, nil
}

func (t *Tools) WriteFile(ctx context.Context, in WriteFileIn) (WriteFileOut, error) {
	sb, err := t.requireSandbox(in.Name)
	if err != nil {
		return WriteFileOut{}, err
	}
	mode := parseMode(in.Mode, 0o644)
	mu := t.sbMutex(sb.Name)
	mu.Lock()
	defer mu.Unlock()

	c, err := t.Connect(ctx, sb)
	if err != nil {
		return WriteFileOut{}, err
	}
	defer c.Close()

	if err := WriteFile(c, in.Path, []byte(in.Content), mode); err != nil {
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

	c, err := t.Connect(ctx, sb)
	if err != nil {
		return ReadFileOut{}, err
	}
	defer c.Close()

	body, truncated, err := ReadFile(c, in.Path, in.MaxBytes)
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

	c, err := t.Connect(ctx, sb)
	if err != nil {
		return ListFilesOut{}, err
	}
	defer c.Close()

	entries, err := ListFiles(c, in.Path, in.Recursive)
	if err != nil {
		return ListFilesOut{}, err
	}
	t.State.BumpActivity(sb.Name, time.Now().UTC())
	_ = t.State.Save()
	return ListFilesOut{Entries: entries}, nil
}

func parseMode(s string, fallback os.FileMode) os.FileMode {
	if s == "" {
		return fallback
	}
	s = strings.TrimPrefix(s, "0o")
	s = strings.TrimPrefix(s, "0")
	var n int
	for _, c := range s {
		if c < '0' || c > '7' {
			return fallback
		}
		n = n*8 + int(c-'0')
	}
	return os.FileMode(n)
}
```

(Add `"os"` to imports.)

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
	created  []sandbox.CreateOpts
	deleted  []string
	stopped  []string
	started  []string
	gets     []string
	getResp  *sandbox.Instance
}

func (f *fakeSandbox) Create(ctx context.Context, o sandbox.CreateOpts) (*sandbox.Instance, error) {
	f.created = append(f.created, o)
	return &sandbox.Instance{Name: o.Name, Status: sandbox.StatusRunning, Addresses: []string{"10.0.0.1"}}, nil
}
func (f *fakeSandbox) Get(ctx context.Context, n string) (*sandbox.Instance, error) {
	f.gets = append(f.gets, n)
	if f.getResp != nil {
		return f.getResp, nil
	}
	return &sandbox.Instance{Name: n, Status: sandbox.StatusRunning, Addresses: []string{"10.0.0.1"}}, nil
}
func (f *fakeSandbox) List(ctx context.Context) ([]sandbox.Instance, error)         { return nil, nil }
func (f *fakeSandbox) Start(ctx context.Context, n string) error                    { f.started = append(f.started, n); return nil }
func (f *fakeSandbox) Stop(ctx context.Context, n string) error                     { f.stopped = append(f.stopped, n); return nil }
func (f *fakeSandbox) Delete(ctx context.Context, n string) error                   { f.deleted = append(f.deleted, n); return nil }
func (f *fakeSandbox) CreateSnapshot(ctx context.Context, n, l string) error        { return nil }
func (f *fakeSandbox) ListSnapshots(ctx context.Context, n string) ([]sandbox.Snapshot, error) { return nil, nil }
func (f *fakeSandbox) DeleteSnapshot(ctx context.Context, n, l string) error        { return nil }
func (f *fakeSandbox) RestoreSnapshot(ctx context.Context, n, l string) error       { return nil }
func (f *fakeSandbox) CloneFrom(ctx context.Context, src, lbl, nn string) error     { return nil }
func (f *fakeSandbox) Run(ctx context.Context, n string, o sandbox.ExecOpts) (int, error) {
	return 0, nil
}
func (f *fakeSandbox) Output(ctx context.Context, n string, c []string) ([]byte, error) { return nil, nil }
func (f *fakeSandbox) Console(ctx context.Context, n string, o sandbox.ConsoleOpts) error { return nil }
func (f *fakeSandbox) Ready(ctx context.Context, n string, t time.Duration) error   { return nil }
func (f *fakeSandbox) SetEgressMode(ctx context.Context, n string, m sandbox.EgressMode) error { return nil }
func (f *fakeSandbox) AllowDomain(ctx context.Context, n, d string) error           { return nil }
func (f *fakeSandbox) DenyDomain(ctx context.Context, n, d string) error            { return nil }
func (f *fakeSandbox) GetPolicy(ctx context.Context, n string) (*sandbox.Policy, error) { return nil, nil }
func (f *fakeSandbox) Capabilities() sandbox.Capabilities                            { return sandbox.Capabilities{} }
func (f *fakeSandbox) Close() error                                                  { return nil }

func newTestTools(t *testing.T) (*Tools, *fakeSandbox) {
	t.Helper()
	dir := t.TempDir()
	s, _ := LoadState(filepath.Join(dir, "s.json"))
	be := &fakeSandbox{}
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

func TestStopUpdatesStatus(t *testing.T) {
	tt, _ := newTestTools(t)
	out, _ := tt.CreateSandbox(context.Background(), CreateSandboxIn{})
	if _, err := tt.StopSandbox(context.Background(), SandboxRef{Name: out.Name}); err != nil {
		t.Fatal(err)
	}
	got, _ := tt.State.Get(out.Name)
	if got.Status != "stopped" {
		t.Errorf("status = %q, want stopped", got.Status)
	}
}
```

**Step 3: Run**

```
go mod tidy
go test ./internal/mcp -v
go build ./...
```

Expected: PASS.

**Step 4: Commit**

```bash
git add internal/mcp/tools.go internal/mcp/tools_test.go go.mod go.sum
git commit -m "feat(mcp): add tool layer with create/start/stop/destroy/exec/files"
```

---

## Task 10: Wire up the MCP server (streamable-HTTP)

**Files:**
- Create: `internal/mcp/server.go`
- Create: `internal/mcp/server_test.go`

**Verify SDK before implementing.** Run:

```
go doc github.com/modelcontextprotocol/go-sdk/mcp NewServer
go doc github.com/modelcontextprotocol/go-sdk/mcp AddTool
go doc github.com/modelcontextprotocol/go-sdk/mcp StreamableHTTPHandler
```

Adapt the calls below to match the current API. The current shape (early 2026) is roughly:

```go
import sdk "github.com/modelcontextprotocol/go-sdk/mcp"

srv := sdk.NewServer(&sdk.Implementation{Name: "pixels-mcp", Version: "0.1.0"}, nil)
sdk.AddTool(srv, &sdk.Tool{Name: "create_sandbox", Description: "..."}, tools.CreateSandbox)
handler := sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server { return srv }, nil)
http.Handle(endpointPath, handler)
```

If `AddTool` expects a different handler shape (e.g. `func(ctx, *CallToolRequest, In) (*CallToolResult, Out, error)`), wrap each `Tools.Foo` in a small adapter that forwards arguments and discards/builds the unused parts.

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
	Connect        SandboxFactory
	Prefix         string
	DefaultImage   string
	ExecTimeoutMax time.Duration
}

// NewServer wires the MCP tool surface and returns an HTTP handler ready to mount.
func NewServer(opts ServerOpts, endpointPath string) (http.Handler, *Tools) {
	tools := &Tools{
		State:          opts.State,
		Backend:        opts.Backend,
		Connect:        opts.Connect,
		Prefix:         opts.Prefix,
		DefaultImage:   opts.DefaultImage,
		ExecTimeoutMax: opts.ExecTimeoutMax,
	}

	srv := sdk.NewServer(&sdk.Implementation{Name: "pixels-mcp", Version: "0.1.0"}, nil)

	// Each call below registers one tool. Adapt the handler signature to the
	// current SDK shape — the underlying Tools method returns (Out, error).
	sdk.AddTool(srv, &sdk.Tool{Name: "create_sandbox", Description: "Create a new ephemeral sandbox container."},
		adapt0(tools.CreateSandbox))
	sdk.AddTool(srv, &sdk.Tool{Name: "destroy_sandbox", Description: "Destroy a sandbox and its filesystem."},
		adapt1(tools.DestroySandbox))
	sdk.AddTool(srv, &sdk.Tool{Name: "start_sandbox", Description: "Start (resume) a stopped sandbox."},
		adapt0(tools.StartSandbox))
	sdk.AddTool(srv, &sdk.Tool{Name: "stop_sandbox", Description: "Stop (pause) a running sandbox."},
		adapt1(tools.StopSandbox))
	sdk.AddTool(srv, &sdk.Tool{Name: "list_sandboxes", Description: "List all tracked sandboxes."},
		adaptList(tools.ListSandboxes))
	sdk.AddTool(srv, &sdk.Tool{Name: "exec", Description: "Run a command inside a sandbox."},
		adapt2(tools.Exec))
	sdk.AddTool(srv, &sdk.Tool{Name: "write_file", Description: "Write a file inside a sandbox."},
		adapt2(tools.WriteFile))
	sdk.AddTool(srv, &sdk.Tool{Name: "read_file", Description: "Read a file from a sandbox."},
		adapt2(tools.ReadFile))
	sdk.AddTool(srv, &sdk.Tool{Name: "list_files", Description: "List files inside a sandbox path."},
		adapt2(tools.ListFiles))

	handler := sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server { return srv }, nil)
	mux := http.NewServeMux()
	mux.Handle(endpointPath, handler)
	return mux, tools
}

// adapt* helpers convert (ctx, In) -> (Out, error) handlers into whatever
// signature the SDK expects. Keep these wrappers tiny and adjust to the SDK
// version in use.

type any2 = any

// Replace the bodies of these helpers with the correct SDK adaptation. They
// exist as named placeholders so the call sites above stay readable. If the
// SDK exposes a generic `func[Args, Result any](ctx, *CallToolRequest, Args)`,
// these become one-liner forwards.
func adapt0[I, O any](fn func(context.Context, I) (O, error)) any { return fn }
func adapt1[I, O any](fn func(context.Context, I) (O, error)) any { return fn }
func adapt2[I, O any](fn func(context.Context, I) (O, error)) any { return fn }
func adaptList[O any](fn func(context.Context) (O, error)) any   { return fn }
```

> **Engineer note:** the `adapt*` helpers are placeholders. Once you've checked the SDK signature with `go doc`, replace them with real wrappers and update the `sdk.AddTool(...)` call signatures. The function bodies above compile only if `AddTool` accepts `any`; in practice you'll define the wrappers to match `func(context.Context, *sdk.CallToolRequest, In) (*sdk.CallToolResult, Out, error)` (or whatever the current SDK exposes), and forward the tool method's return values into that shape.

**Step 2: Write a smoke test**

Create `internal/mcp/server_test.go`:

```go
package mcp

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestNewServerMountsHandler(t *testing.T) {
	dir := t.TempDir()
	st, _ := LoadState(filepath.Join(dir, "s.json"))
	be := &fakeSandbox{}

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
	// We don't assert on the body — many MCP SDKs reject non-MCP-handshake
	// GETs with 4xx. The check is just "endpoint mounted, server didn't 500
	// on a stray request".
	if resp.StatusCode >= 500 {
		t.Errorf("unexpected 5xx: %d", resp.StatusCode)
	}
}
```

(Add `"net/http"` to imports.)

**Step 3: Run**

```
go test ./internal/mcp -v
go build ./...
```

Expected: PASS.

**Step 4: Commit**

```bash
git add internal/mcp/server.go internal/mcp/server_test.go go.mod go.sum
git commit -m "feat(mcp): wire streamable-HTTP server with tool registrations"
```

---

## Task 11: Add cobra `pixels mcp` subcommand

**Files:**
- Create: `cmd/mcp.go`
- Modify: `cmd/root.go` (only if needed — usually no change)

**Step 1: Implement the command**

Create `cmd/mcp.go`:

```go
package cmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/deevus/pixels/internal/config"
	mcppkg "github.com/deevus/pixels/internal/mcp"
	"github.com/deevus/pixels/internal/sshclient"
	"github.com/deevus/pixels/sandbox"
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

	connect := makeConnectFn(cfg)

	mux, _ := mcppkg.NewServer(mcppkg.ServerOpts{
		State:          state,
		Backend:        sb,
		Connect:        connect,
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

	srv := &http.Server{
		Addr:    listenAddr,
		Handler: mux,
	}

	// non-loopback warning
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

func makeConnectFn(cfg *config.Config) mcppkg.SandboxFactory {
	return func(ctx context.Context, sb mcppkg.Sandbox) (*sshclient.Client, error) {
		return sshclient.NewClient(ctx, sshclient.Config{
			Host:           sb.IP,
			User:           cfg.SSH.User,
			KeyPath:        cfg.SSH.Key,
			KnownHostsPath: config.KnownHostsPath(),
			Timeout:        10 * time.Second,
		})
	}
}

func pickStr(override, fallback string) string {
	if override != "" {
		return override
	}
	return fallback
}

func isLoopback(addr string) bool {
	return len(addr) >= 3 && (addr[:3] == "127" || addr[:9] == "localhost")
}
```

> **Engineer note:** if the existing root command stores config under a different context key, replace `configKey` accordingly. Look at `cmd/root.go`'s `PersistentPreRunE` to see how config is propagated to subcommands and reuse the same key.

**Step 2: Build and confirm the subcommand registers**

```
go build -o pixels .
./pixels mcp --help
```

Expected: help text printed, including `--listen-addr`, `--state-file`, `--pid-file` flags.

**Step 3: Run unit tests**

```
go test ./... -v
```

Expected: PASS.

**Step 4: Commit**

```bash
git add cmd/mcp.go
git commit -m "feat(cmd): add 'pixels mcp' subcommand"
```

---

## Task 12: End-to-end smoke documentation

**Files:**
- Modify: `README.md` (add a "Using as MCP server" section)

**Step 1: Append to `README.md`**

Add a new top-level section:

```markdown
## Using `pixels` as an MCP code-sandbox server

`pixels mcp` runs a streamable-HTTP MCP server that exposes container
lifecycle and code execution as MCP tools. Run it once on your machine,
then point any number of MCP clients at it.

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
| `write_file` / `read_file` / `list_files` | SFTP-backed file I/O |

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

Try a real client run by pointing Claude Code at the URL and calling `create_sandbox` from a chat. Confirm:

- Container appears in `pixels list`.
- `exec` works (e.g. `python3 -c 'print(1)'`).
- `write_file` followed by `read_file` round-trips content.
- After `destroy_sandbox`, the container is gone from `pixels list`.

**Step 3: Commit**

```bash
git add README.md
git commit -m "docs: document 'pixels mcp' server usage and tool surface"
```

---

## Task 13: Final cleanup

**Step 1: Run the full test + build matrix**

```
go test ./... -race
go build ./...
go vet ./...
```

All three should pass clean.

**Step 2: Confirm no leftover TODOs or `panic("not implemented")`**

```
grep -rn "TODO\|FIXME\|panic(\"not implemented\")" internal/mcp internal/sshclient cmd/mcp.go
```

Expected: no output (or only intentional TODOs you want to keep).

**Step 3: Update `MEMORY.md` (optional)**

If anything surprising came up during implementation that should persist
across sessions, save a feedback or project memory.

**Step 4: Final commit only if anything changed**

If the cleanup pass produced edits:

```bash
git commit -am "chore(mcp): final cleanup"
```

Otherwise nothing to do.

---

## Out of scope for v1

These are explicitly *not* in this plan and should be left alone:

- Auth tokens / TLS — loopback-only is the trust boundary.
- Checkpoint/restore as MCP tools.
- Network-policy MCP tools.
- A `pixels mcp doctor` subcommand.
- Streaming `read_file_chunk(offset, length)`.

If you find yourself wanting one of these, stop and check with the user
first.
