# Bases as Containers — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the snapshot-based base-pixel lifecycle with the container-as-base model from the design doc. Ship embedded defaults (`dev`, `python`, `node`) so first-run is zero-config. Add base-from-base inheritance. Reuse existing `pixels checkpoint create` for publishing — no `commit` or `modify` verbs.

**Architecture:** Bases are plain containers named `<base_prefix><name>`. Sandboxes clone from the latest-by-time checkpoint of the base container (existing `Backend.CloneFrom(name, label, newName)`). `BuildBase` either creates from `parent_image` or clones from a parent base's latest checkpoint when `from = "<base>"` is set. Cascade-build walks the dependency chain on demand. Two new CLI verbs: `pixels base list/build`. Existing primitives handle deletion, mutation, and publishing.

**Tech Stack:**
- Go 1.25 (`embed`, `time.Time`)
- existing: `sandbox/`, `internal/mcp/`, `internal/config/`, `cmd/`
- no new dependencies

**Worktree:** `/Users/sh/Projects/pixels/.worktrees/mcp-sandbox` on `feat/mcp-sandbox-server`. HEAD is `78ce774` (the design-doc commit) at plan time.

**VCS:** This worktree uses git directly (the user has been committing via git in the worktree). Each task ends with `git add ... && git commit -m "..."`. Do NOT add `Co-Authored-By` lines — the user disables those globally.

---

## Engineer-orientation notes

Before starting:

1. **Read the design doc** at `docs/plans/2026-04-27-bases-as-containers-design.md`. Every architectural decision in this plan is justified there. The plan locks one path per decision; do not reintroduce alternatives.
2. **Read** `sandbox/sandbox.go` — `Backend`/`Exec`/`Files`/`NetworkPolicy` interfaces composed into `Sandbox`. You'll be removing one method (`SnapshotExists`) and adding a field to `Snapshot`.
3. **Read** `internal/mcp/builder.go`, `buildbase.go`, `tools.go` — these are the largest changes. Note where `BuilderContainerName` and `SnapshotName` are used; both go away.
4. **Read** `internal/config/config.go` — adds new fields and a validation pass.
5. **Read** `cmd/checkpoint.go` — the existing `pixels checkpoint create` is the publish mechanism for bases. You don't need to modify it; just understand what it does.

**Test conventions:** table-driven; existing patterns in `internal/mcp/*_test.go` and `internal/config/config_test.go`.

**Commit cadence:** one commit per task. Single-line commit messages with `type(scope): description` (existing convention in the repo).

**Locked decisions (do not reopen):**

- Initial checkpoint label after `BuildBase`: `"initial"` (string constant `internal/mcp.InitialCheckpointLabel`).
- `mcp:` script scheme: any `setup_script` value starting with `mcp:` is read from `defaults.DefaultsFS` (stripping the prefix); anything else is read from disk via `os.ReadFile`.
- Default base prefix: `"px-base-"`. Configurable via `mcp.base_prefix` / `PIXELS_MCP_BASE_PREFIX`.
- `Backend.SnapshotExists` is removed entirely. Existence checks use `Backend.Get` + `errors.Is(err, sandbox.ErrNotFound)` (the existing pattern after the `WrapNotFound` refactor).
- Cascade-build is implemented as `internal/mcp.BuildChain(ctx, cfg, builder, target, exists)` — walks `from` chain bottom-up, builds missing links via `Builder.Build`.
- Latest-checkpoint lookup is implemented as `internal/mcp.latestCheckpoint([]sandbox.Snapshot) (sandbox.Snapshot, bool)` — picks max `CreatedAt`.

---

## Task 1: Add `Base.From` and `MCP.BasePrefix` config fields

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Step 1: Write the failing tests**

Append to `internal/config/config_test.go`:

```go
func TestMCPBasePrefixDefault(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.MCP.BasePrefix, "px-base-"; got != want {
		t.Errorf("BasePrefix = %q, want %q", got, want)
	}
}

func TestMCPBasePrefixEnvOverride(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("PIXELS_MCP_BASE_PREFIX", "custom-")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.MCP.BasePrefix, "custom-"; got != want {
		t.Errorf("BasePrefix = %q, want %q", got, want)
	}
}

func TestBaseFromField(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	cfgPath := filepath.Join(tmpDir, "pixels", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(`
[mcp.bases.dev]
parent_image = "images:ubuntu/24.04"
setup_script = "/tmp/dev.sh"
description = "Dev"

[mcp.bases.python]
from = "dev"
setup_script = "/tmp/python.sh"
description = "Python"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.MCP.Bases["python"].From; got != "dev" {
		t.Errorf("python.From = %q, want dev", got)
	}
	if got := cfg.MCP.Bases["dev"].ParentImage; got != "images:ubuntu/24.04" {
		t.Errorf("dev.ParentImage = %q", got)
	}
}
```

**Step 2: Run to verify failure**

```
go test ./internal/config -run "TestMCPBasePrefix|TestBaseFromField" -v
```

Expected: FAIL — `BasePrefix` and `From` undefined.

**Step 3: Implement**

In `internal/config/config.go`, add to the `MCP` struct:

```go
BasePrefix string `toml:"base_prefix" env:"PIXELS_MCP_BASE_PREFIX"`
```

Default it in the `cfg := &Config{...}` block in `Load()`:

```go
MCP: MCP{
    Prefix:           "px-mcp-",
    BasePrefix:       "px-base-",
    // ... existing defaults ...
},
```

Add `From` to the `Base` struct:

```go
type Base struct {
	ParentImage string `toml:"parent_image"`
	From        string `toml:"from"`
	SetupScript string `toml:"setup_script"`
	Description string `toml:"description"`
}
```

**Step 4: Run tests**

```
go test ./internal/config -v
go build ./...
```

Expected: PASS, build clean.

**Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add Base.From and MCP.BasePrefix fields"
```

---

## Task 2: Add `parent_image` / `from` mutual-exclusion + cycle detection at config load

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Step 1: Write the failing tests**

Append:

```go
func TestBaseRejectsBothParentImageAndFrom(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	cfgPath := filepath.Join(tmpDir, "pixels", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(`
[mcp.bases.bad]
parent_image = "images:ubuntu/24.04"
from = "dev"
setup_script = "/tmp/x.sh"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load()
	if err == nil {
		t.Fatal("expected Load to error on base with both parent_image and from")
	}
}

func TestBaseRejectsNeitherParentImageNorFrom(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	cfgPath := filepath.Join(tmpDir, "pixels", "config.toml")
	_ = os.MkdirAll(filepath.Dir(cfgPath), 0o755)
	_ = os.WriteFile(cfgPath, []byte(`
[mcp.bases.bad]
setup_script = "/tmp/x.sh"
`), 0o644)
	_, err := Load()
	if err == nil {
		t.Fatal("expected Load to error on base with neither parent_image nor from")
	}
}

func TestBaseRejectsFromMissingTarget(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	cfgPath := filepath.Join(tmpDir, "pixels", "config.toml")
	_ = os.MkdirAll(filepath.Dir(cfgPath), 0o755)
	_ = os.WriteFile(cfgPath, []byte(`
[mcp.bases.python]
from = "doesnotexist"
setup_script = "/tmp/x.sh"
`), 0o644)
	_, err := Load()
	if err == nil {
		t.Fatal("expected Load to error on base whose from references a missing base")
	}
}

func TestBaseRejectsCycle(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	cfgPath := filepath.Join(tmpDir, "pixels", "config.toml")
	_ = os.MkdirAll(filepath.Dir(cfgPath), 0o755)
	_ = os.WriteFile(cfgPath, []byte(`
[mcp.bases.a]
from = "b"
setup_script = "/tmp/x.sh"

[mcp.bases.b]
from = "a"
setup_script = "/tmp/x.sh"
`), 0o644)
	_, err := Load()
	if err == nil {
		t.Fatal("expected Load to error on base cycle")
	}
}

func TestBaseAcceptsValidChain(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	cfgPath := filepath.Join(tmpDir, "pixels", "config.toml")
	_ = os.MkdirAll(filepath.Dir(cfgPath), 0o755)
	_ = os.WriteFile(cfgPath, []byte(`
[mcp.bases.dev]
parent_image = "images:ubuntu/24.04"
setup_script = "/tmp/dev.sh"

[mcp.bases.python]
from = "dev"
setup_script = "/tmp/python.sh"

[mcp.bases.django]
from = "python"
setup_script = "/tmp/django.sh"
`), 0o644)
	_, err := Load()
	if err != nil {
		t.Fatalf("expected valid chain to load, got: %v", err)
	}
}
```

**Step 2: Run to verify failure**

```
go test ./internal/config -run "TestBaseRejects|TestBaseAcceptsValidChain" -v
```

Expected: FAIL — validation logic missing.

**Step 3: Implement**

In `internal/config/config.go`, add a validation function and call it from `Load()` before returning:

```go
// validateBases checks that every [mcp.bases] entry declares exactly one of
// parent_image or from, that every from references an existing base, and
// that the dependency graph has no cycles.
func validateBases(bases map[string]Base) error {
	if len(bases) == 0 {
		return nil
	}
	for name, b := range bases {
		hasParent := b.ParentImage != ""
		hasFrom := b.From != ""
		if hasParent && hasFrom {
			return fmt.Errorf("mcp.bases.%s: parent_image and from are mutually exclusive", name)
		}
		if !hasParent && !hasFrom {
			return fmt.Errorf("mcp.bases.%s: must declare exactly one of parent_image or from", name)
		}
		if hasFrom {
			if _, ok := bases[b.From]; !ok {
				return fmt.Errorf("mcp.bases.%s: from references unknown base %q", name, b.From)
			}
		}
	}
	// Cycle detection via DFS with white/gray/black coloring.
	const (
		white = 0
		gray  = 1
		black = 2
	)
	colour := make(map[string]int, len(bases))
	var visit func(name, path string) error
	visit = func(name, path string) error {
		switch colour[name] {
		case gray:
			return fmt.Errorf("mcp.bases: cycle detected: %s -> %s", path, name)
		case black:
			return nil
		}
		colour[name] = gray
		if from := bases[name].From; from != "" {
			if err := visit(from, path+" -> "+name); err != nil {
				return err
			}
		}
		colour[name] = black
		return nil
	}
	for name := range bases {
		if err := visit(name, ""); err != nil {
			return err
		}
	}
	return nil
}
```

In `Load()`, after `resolveEnv(cfg)` and after `expandHome` calls, before `return cfg, nil`:

```go
if err := validateBases(cfg.MCP.Bases); err != nil {
	return nil, err
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
git commit -m "feat(config): validate bases (mutex parent_image/from, cycles, missing refs)"
```

---

## Task 3: Add `Snapshot.CreatedAt` and update both backends

**Files:**
- Modify: `sandbox/sandbox.go`
- Modify: `sandbox/incus/backend.go`
- Modify: `sandbox/truenas/backend.go`
- Test: `sandbox/incus/backend_test.go`, `sandbox/truenas/backend_test.go` (existing)

**Step 1: Add the field**

In `sandbox/sandbox.go`, locate `type Snapshot struct {...}` and add:

```go
type Snapshot struct {
	Label     string
	Size      int64
	CreatedAt time.Time
}
```

(Add `"time"` to imports if not present.)

**Step 2: Update Incus backend**

In `sandbox/incus/backend.go`, find `ListSnapshots`. The Incus client returns snapshot info that includes a `CreatedAt time.Time` (in `api.InstanceSnapshot`). Populate it:

```go
out = append(out, sandbox.Snapshot{
	Label:     s.Name,
	Size:      0, // existing
	CreatedAt: s.CreatedAt, // NEW
})
```

(Adapt to the actual upstream field name — verify with `go doc github.com/lxc/incus/v6/shared/api.InstanceSnapshot`.)

**Step 3: Update TrueNAS backend**

In `sandbox/truenas/backend.go`, find `ListSnapshots`. ZFS snapshots have a `creation` timestamp accessible via the truenas-go ZFS API. Populate `CreatedAt` from it. If the upstream returns Unix seconds, convert via `time.Unix(secs, 0).UTC()`.

**Step 4: Run tests**

```
go build ./...
go test ./sandbox/... -v
```

Expected: PASS, build clean. The existing tests don't yet assert on `CreatedAt`; that's exercised in Task 9.

**Step 5: Commit**

```bash
git add sandbox/sandbox.go sandbox/incus/backend.go sandbox/truenas/backend.go
git commit -m "feat(sandbox): add Snapshot.CreatedAt populated by both backends"
```

---

## Task 4: Remove `Backend.SnapshotExists` and switch callers to `Get` + `ErrNotFound`

**Files:**
- Modify: `sandbox/sandbox.go`
- Modify: `sandbox/incus/backend.go`
- Modify: `sandbox/truenas/backend.go`
- Modify: `internal/mcp/tools.go`
- Modify: `sandbox/truenas/backend_test.go` (test removal)

**Step 1: Drop the interface method**

In `sandbox/sandbox.go`, remove:

```go
SnapshotExists(ctx context.Context, instanceName, label string) (bool, error)
```

**Step 2: Drop the implementations**

In `sandbox/incus/backend.go` and `sandbox/truenas/backend.go`, remove the `SnapshotExists` method on each backend.

**Step 3: Update callers in `internal/mcp/tools.go`**

Find every call to `t.Backend.SnapshotExists(...)`. Replace with the `Get`-and-check pattern. There are typically two call sites: `provisionFromBase` and `ListBases`.

```go
// Before
exists, err := t.Backend.SnapshotExists(ctx, builderName, snapName)
if err != nil { ... }
if exists { ... }

// After: check that the BASE container itself exists. Snapshot existence
// becomes irrelevant — we list checkpoints on the base in Task 9.
_, err := t.Backend.Get(ctx, baseName)
exists := err == nil
if err != nil && !errors.Is(sandbox.WrapNotFound(err), sandbox.ErrNotFound) {
	return ..., fmt.Errorf("get base %s: %w", baseName, err)
}
if exists { ... }
```

(Note: the precise call-site rewrite is finalised in Task 9. For now, just delete the `SnapshotExists` calls and leave a placeholder TODO comment so the package still compiles. Use `_ = baseName` or comparable to silence unused warnings if needed.)

> **Engineer note:** the goal of this task is removing the interface method without leaving dangling references. If full re-wiring of `provisionFromBase` and `ListBases` is easier in one pass, do it here and skip the placeholder. Either way, after this task, no test should fail and the build should be clean.

**Step 4: Drop the snapshot-exists test**

In `sandbox/truenas/backend_test.go`, remove any test that exercises `SnapshotExists` directly (the previous regression test). The contract is gone; the test is moot.

**Step 5: Verify**

```
grep -rn "SnapshotExists" --include="*.go"
```

Expected: empty.

```
go build ./...
go test ./... -race
```

Expected: PASS, build clean.

**Step 6: Commit**

```bash
git add sandbox/sandbox.go sandbox/incus/backend.go sandbox/truenas/backend.go internal/mcp/tools.go sandbox/truenas/backend_test.go
git commit -m "refactor(sandbox): drop Backend.SnapshotExists; use Get + ErrNotFound"
```

---

## Task 5: Embedded default scripts and `internal/mcp/defaults.go`

**Files:**
- Create: `internal/mcp/bases/dev.sh`
- Create: `internal/mcp/bases/python.sh`
- Create: `internal/mcp/bases/node.sh`
- Create: `internal/mcp/defaults.go`
- Test: `internal/mcp/defaults_test.go`

**Step 1: Create the scripts**

`internal/mcp/bases/dev.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends \
  git curl wget jq vim ca-certificates build-essential
apt-get clean
rm -rf /var/lib/apt/lists/*
```

`internal/mcp/bases/python.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends \
  python3 python3-pip python3-venv pipx
apt-get clean
rm -rf /var/lib/apt/lists/*
```

`internal/mcp/bases/node.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ca-certificates curl gnupg
mkdir -p /etc/apt/keyrings
curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key \
  | gpg --dearmor -o /etc/apt/keyrings/nodesource.gpg
echo "deb [signed-by=/etc/apt/keyrings/nodesource.gpg] https://deb.nodesource.com/node_22.x nodistro main" \
  > /etc/apt/sources.list.d/nodesource.list
apt-get update
apt-get install -y --no-install-recommends nodejs
apt-get clean
rm -rf /var/lib/apt/lists/*
```

Make them executable in source (mode 0755).

**Step 2: Write the failing test**

Create `internal/mcp/defaults_test.go`:

```go
package mcp

import (
	"strings"
	"testing"
)

func TestDefaultBasesPresent(t *testing.T) {
	for _, name := range []string{"dev", "python", "node"} {
		if _, ok := DefaultBases[name]; !ok {
			t.Errorf("DefaultBases missing %q", name)
		}
	}
}

func TestDefaultBaseScriptsLoadable(t *testing.T) {
	for name, b := range DefaultBases {
		if !strings.HasPrefix(b.SetupScript, "mcp:") {
			t.Errorf("base %q: SetupScript = %q, want mcp: prefix", name, b.SetupScript)
		}
		path := strings.TrimPrefix(b.SetupScript, "mcp:")
		body, err := DefaultsFS.ReadFile(path)
		if err != nil {
			t.Errorf("base %q: cannot read embedded script %q: %v", name, path, err)
		}
		if len(body) == 0 {
			t.Errorf("base %q: embedded script %q is empty", name, path)
		}
		if !strings.HasPrefix(string(body), "#!") {
			t.Errorf("base %q: script does not start with shebang", name)
		}
	}
}

func TestDefaultBaseChain(t *testing.T) {
	if got := DefaultBases["dev"].ParentImage; got == "" {
		t.Error("dev should have parent_image (root of the chain)")
	}
	if got := DefaultBases["python"].From; got != "dev" {
		t.Errorf("python.From = %q, want dev", got)
	}
	if got := DefaultBases["node"].From; got != "dev" {
		t.Errorf("node.From = %q, want dev", got)
	}
}
```

**Step 3: Run to verify failure**

```
go test ./internal/mcp -run TestDefault -v
```

Expected: FAIL — `DefaultBases` undefined.

**Step 4: Implement**

Create `internal/mcp/defaults.go`:

```go
package mcp

import (
	"embed"

	"github.com/deevus/pixels/internal/config"
)

//go:embed bases/*.sh
var DefaultsFS embed.FS

// DefaultBases is the set of bases shipped with the binary. Loaded into
// config.MCP.Bases at startup if the user has not declared a base of the
// same name. User config wins on name conflict — no field merge.
var DefaultBases = map[string]config.Base{
	"dev": {
		ParentImage: "images:ubuntu/24.04",
		SetupScript: "mcp:bases/dev.sh",
		Description: "Ubuntu 24.04 + git, curl, wget, jq, vim, build-essential",
	},
	"python": {
		From:        "dev",
		SetupScript: "mcp:bases/python.sh",
		Description: "dev + python3, pip, pipx, venv",
	},
	"node": {
		From:        "dev",
		SetupScript: "mcp:bases/node.sh",
		Description: "dev + Node 22 LTS, npm",
	},
}
```

**Step 5: Run tests**

```
go test ./internal/mcp -run TestDefault -v
go build ./...
```

Expected: PASS.

**Step 6: Commit**

```bash
git add internal/mcp/bases/ internal/mcp/defaults.go internal/mcp/defaults_test.go
git commit -m "feat(mcp): embed default bases (dev, python, node)"
```

---

## Task 6: Merge defaults into config at `Load()`

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

> **Engineer note:** this task introduces a circular-import risk: `internal/config` cannot import `internal/mcp` (mcp depends on config). The defaults map must live somewhere both packages can see, OR the merge happens in mcp at daemon startup, OR the defaults map is duplicated in config. **The cleanest fix:** move `DefaultBases` from `internal/mcp/defaults.go` to `internal/config/defaults.go` (same package), since the type `config.Base` already lives there. Then `internal/mcp/defaults.go` only holds `DefaultsFS embed.FS` (the embedded scripts) and re-exports the merged set if needed. Do that move as part of this task.

**Step 1: Move `DefaultBases` to `internal/config`**

Move the `DefaultBases` map definition from `internal/mcp/defaults.go` to a new file `internal/config/defaults.go`:

```go
package config

// DefaultBases is the set of bases shipped with the binary. Lives here
// (not internal/mcp) so config.Load() can merge them without importing
// internal/mcp (which would be a cycle). The embedded script *files*
// stay in internal/mcp/defaults.go via //go:embed.
var DefaultBases = map[string]Base{
	"dev": {
		ParentImage: "images:ubuntu/24.04",
		SetupScript: "mcp:bases/dev.sh",
		Description: "Ubuntu 24.04 + git, curl, wget, jq, vim, build-essential",
	},
	"python": {
		From:        "dev",
		SetupScript: "mcp:bases/python.sh",
		Description: "dev + python3, pip, pipx, venv",
	},
	"node": {
		From:        "dev",
		SetupScript: "mcp:bases/node.sh",
		Description: "dev + Node 22 LTS, npm",
	},
}
```

In `internal/mcp/defaults.go`, drop the `DefaultBases` declaration. Keep only:

```go
package mcp

import "embed"

//go:embed bases/*.sh
var DefaultsFS embed.FS
```

Update `internal/mcp/defaults_test.go` to use `config.DefaultBases` instead of `mcp.DefaultBases`. Add `"github.com/deevus/pixels/internal/config"` to its imports.

**Step 2: Write the failing test for merge behaviour**

Append to `internal/config/config_test.go`:

```go
func TestDefaultBasesMergedWhenAbsentFromUserConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dev", "python", "node"} {
		if _, ok := cfg.MCP.Bases[name]; !ok {
			t.Errorf("default base %q should be present after Load with no user config", name)
		}
	}
}

func TestUserConfigReplacesDefault(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	cfgPath := filepath.Join(tmpDir, "pixels", "config.toml")
	_ = os.MkdirAll(filepath.Dir(cfgPath), 0o755)
	_ = os.WriteFile(cfgPath, []byte(`
[mcp.bases.python]
parent_image = "images:debian/12"
setup_script = "/tmp/my-python.sh"
description = "my python"
`), 0o644)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.MCP.Bases["python"]
	if got.ParentImage != "images:debian/12" {
		t.Errorf("user config did not win: ParentImage = %q", got.ParentImage)
	}
	if got.From != "" {
		t.Errorf("user config did not fully replace default: From = %q (default had from='dev')", got.From)
	}
	if got.Description != "my python" {
		t.Errorf("Description = %q", got.Description)
	}
	// Other defaults still present
	if _, ok := cfg.MCP.Bases["dev"]; !ok {
		t.Errorf("dev (untouched default) should still be present")
	}
}
```

**Step 3: Run to verify failure**

```
go test ./internal/config -run "TestDefaultBasesMerged|TestUserConfigReplacesDefault" -v
```

Expected: FAIL — defaults aren't merged yet.

**Step 4: Implement merge in `Load()`**

In `internal/config/config.go` `Load()`, after the TOML decode and env parse but **before** `validateBases`, merge defaults:

```go
if cfg.MCP.Bases == nil {
	cfg.MCP.Bases = make(map[string]Base)
}
for name, b := range DefaultBases {
	if _, ok := cfg.MCP.Bases[name]; ok {
		continue // user config wins
	}
	cfg.MCP.Bases[name] = b
}
```

**Step 5: Run tests**

```
go test ./internal/config -v
go build ./...
```

Expected: PASS.

**Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go internal/config/defaults.go internal/mcp/defaults.go internal/mcp/defaults_test.go
git commit -m "feat(config): merge DefaultBases into MCP.Bases at Load (user wins)"
```

---

## Task 7: Add `BaseName` helper and remove old name helpers

**Files:**
- Modify: `internal/mcp/tools.go` (or wherever `BuilderContainerName`/`SnapshotName` live)
- Create: `internal/mcp/names.go`
- Test: `internal/mcp/names_test.go`

**Step 1: Locate the existing helpers**

```
grep -n "BuilderContainerName\|func SnapshotName\|func BaseName" internal/mcp/*.go
```

Both helpers should be in `internal/mcp/builder.go` or `tools.go`.

**Step 2: Write the failing test**

Create `internal/mcp/names_test.go`:

```go
package mcp

import (
	"testing"

	"github.com/deevus/pixels/internal/config"
)

func TestBaseNameAppliesPrefix(t *testing.T) {
	cfg := &config.Config{MCP: config.MCP{BasePrefix: "px-base-"}}
	if got, want := BaseName(cfg, "python"), "px-base-python"; got != want {
		t.Errorf("BaseName = %q, want %q", got, want)
	}
}

func TestBaseNameRespectsCustomPrefix(t *testing.T) {
	cfg := &config.Config{MCP: config.MCP{BasePrefix: "myco-"}}
	if got, want := BaseName(cfg, "python"), "myco-python"; got != want {
		t.Errorf("BaseName = %q, want %q", got, want)
	}
}

func TestBaseNameDefaultsToPxBaseWhenEmpty(t *testing.T) {
	cfg := &config.Config{MCP: config.MCP{}}
	if got, want := BaseName(cfg, "python"), "px-base-python"; got != want {
		t.Errorf("BaseName = %q, want %q (empty prefix should fall back to px-base-)", got, want)
	}
}
```

**Step 3: Run to verify failure**

```
go test ./internal/mcp -run TestBaseName -v
```

Expected: FAIL.

**Step 4: Implement**

Create `internal/mcp/names.go`:

```go
package mcp

import "github.com/deevus/pixels/internal/config"

// DefaultBasePrefix is the fallback when cfg.MCP.BasePrefix is empty.
const DefaultBasePrefix = "px-base-"

// BaseName returns the container name for a base. The prefix is taken from
// cfg.MCP.BasePrefix, falling back to DefaultBasePrefix when empty (e.g. in
// tests that build a Config by hand).
func BaseName(cfg *config.Config, name string) string {
	prefix := cfg.MCP.BasePrefix
	if prefix == "" {
		prefix = DefaultBasePrefix
	}
	return prefix + name
}
```

**Step 5: Replace old helpers**

Find every call to `BuilderContainerName(...)` and `SnapshotName(...)` and replace with `BaseName(t.Cfg, ...)` (in handlers that have access to `t.Cfg`) or threaded `cfg`.

Delete the old helper definitions.

```
grep -n "BuilderContainerName\|SnapshotName(" internal/mcp/
```

Expected: empty.

**Step 6: Run tests**

```
go test ./internal/mcp -v
go build ./...
```

Expected: PASS, build clean.

**Step 7: Commit**

```bash
git add internal/mcp/
git commit -m "refactor(mcp): collapse BuilderContainerName/SnapshotName to BaseName"
```

---

## Task 8: Rewrite `BuildBase` for `from`, embedded scripts, initial checkpoint

**Files:**
- Modify: `internal/mcp/buildbase.go`
- Modify: `internal/mcp/buildbase_test.go`

**Step 1: Add the InitialCheckpointLabel constant**

At the top of `internal/mcp/buildbase.go`:

```go
// InitialCheckpointLabel is the label of the checkpoint created by BuildBase
// after the setup script runs. Sandboxes spawned right after build clone
// from this. Subsequent user `pixels checkpoint create` calls produce
// timestamped labels; the daemon picks by CreatedAt, not label.
const InitialCheckpointLabel = "initial"
```

**Step 2: Write the failing tests**

In `internal/mcp/buildbase_test.go`, add:

```go
func TestBuildBaseFromParentImage(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "setup.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	be := newFakeSandbox()
	cfg := &config.Config{MCP: config.MCP{BasePrefix: "px-base-"}}
	var buf bytes.Buffer
	err := BuildBase(context.Background(), be, cfg, "python", config.Base{
		ParentImage: "images:ubuntu/24.04",
		SetupScript: scriptPath,
	}, &buf)
	if err != nil {
		t.Fatalf("BuildBase: %v", err)
	}

	if len(be.created) != 1 || be.created[0].Image != "images:ubuntu/24.04" {
		t.Errorf("expected one container created from parent_image; got %v", be.created)
	}
	if be.created[0].Name != "px-base-python" {
		t.Errorf("container name = %q, want px-base-python", be.created[0].Name)
	}
	if got := be.snapshots["px-base-python"]; got == "" {
		t.Errorf("expected initial checkpoint on px-base-python; got snapshots=%v", be.snapshots)
	}
}

func TestBuildBaseFromParentBase(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "setup.sh")
	_ = os.WriteFile(scriptPath, []byte("#!/bin/bash\necho hi\n"), 0o755)

	be := newFakeSandbox()
	cfg := &config.Config{MCP: config.MCP{BasePrefix: "px-base-"}}

	// Pretend the parent base "dev" already exists with one checkpoint.
	be.containers["px-base-dev"] = sandbox.Instance{Name: "px-base-dev", Status: sandbox.StatusStopped}
	be.snapshots["px-base-dev:initial"] = time.Now()

	var buf bytes.Buffer
	err := BuildBase(context.Background(), be, cfg, "python", config.Base{
		From:        "dev",
		SetupScript: scriptPath,
	}, &buf)
	if err != nil {
		t.Fatalf("BuildBase: %v", err)
	}

	if len(be.cloned) != 1 {
		t.Fatalf("expected one clone-from operation; got %v", be.cloned)
	}
	if be.cloned[0].source != "px-base-dev" {
		t.Errorf("clone source = %q, want px-base-dev", be.cloned[0].source)
	}
	if be.cloned[0].dest != "px-base-python" {
		t.Errorf("clone dest = %q, want px-base-python", be.cloned[0].dest)
	}
}

func TestBuildBaseEmbeddedSetupScript(t *testing.T) {
	be := newFakeSandbox()
	cfg := &config.Config{MCP: config.MCP{BasePrefix: "px-base-"}}
	var buf bytes.Buffer
	err := BuildBase(context.Background(), be, cfg, "dev", config.Base{
		ParentImage: "images:ubuntu/24.04",
		SetupScript: "mcp:bases/dev.sh",
	}, &buf)
	if err != nil {
		t.Fatalf("BuildBase with embedded script: %v", err)
	}
	// Confirm the embedded script ran (fakeSandbox.Run records the cmd)
	var sawSetup bool
	for _, c := range be.runs {
		if strings.Contains(strings.Join(c, " "), "bash") {
			sawSetup = true
		}
	}
	if !sawSetup {
		t.Error("expected a bash setup-script run on the new base")
	}
}
```

(Update `fakeSandbox` if needed: add `containers map[string]sandbox.Instance`, `snapshots map[string]time.Time` (key `<container>:<label>`), `cloned []cloneCall{source,dest}`, `runs [][]string` for capturing exec invocations.)

**Step 3: Run to verify failure**

```
go test ./internal/mcp -run TestBuildBase -v
```

Expected: FAIL.

**Step 4: Implement**

Replace the body of `BuildBase` in `internal/mcp/buildbase.go`:

```go
// BuildBase materialises a base container.
//
// If baseCfg.From is set: clone from the parent base's latest checkpoint into
// <BaseName(cfg, name)>. Otherwise create from baseCfg.ParentImage. Run the
// setup script in the new container, stop it, then create the initial
// checkpoint labelled InitialCheckpointLabel.
//
// out receives setup-script stdout/stderr for streaming to the user (when
// invoked from the CLI) or the daemon log (when invoked via cascade build).
func BuildBase(ctx context.Context, be sandbox.Sandbox, cfg *config.Config, name string, baseCfg config.Base, out io.Writer) error {
	scriptBytes, err := loadSetupScript(baseCfg.SetupScript)
	if err != nil {
		return fmt.Errorf("base %s: load setup script: %w", name, err)
	}

	target := BaseName(cfg, name)

	// Build the container — either fresh from image or cloned from parent base.
	if baseCfg.From != "" {
		parentName := BaseName(cfg, baseCfg.From)
		latest, ok, err := latestCheckpointFor(ctx, be, parentName)
		if err != nil {
			return fmt.Errorf("base %s: lookup parent checkpoint on %s: %w", name, parentName, err)
		}
		if !ok {
			return fmt.Errorf("base %s: parent %s has no checkpoints; build the parent first", name, parentName)
		}
		if err := be.CloneFrom(ctx, parentName, latest.Label, target); err != nil {
			return fmt.Errorf("base %s: clone from %s: %w", name, parentName, err)
		}
	} else if baseCfg.ParentImage != "" {
		if _, err := be.Create(ctx, sandbox.CreateOpts{Name: target, Image: baseCfg.ParentImage}); err != nil {
			return fmt.Errorf("base %s: create from image %s: %w", name, baseCfg.ParentImage, err)
		}
	} else {
		return fmt.Errorf("base %s: neither parent_image nor from set", name)
	}

	cleanup := func() {
		if err := be.Delete(context.Background(), target); err != nil {
			fmt.Fprintf(out, "WARN: cleanup of %s after failure: %v\n", target, err)
		}
	}

	if err := be.Ready(ctx, target, 5*time.Minute); err != nil {
		cleanup()
		return fmt.Errorf("base %s: ready: %w", name, err)
	}

	// Upload + run setup script.
	if err := be.WriteFile(ctx, target, "/tmp/pixels-setup.sh", scriptBytes, 0o755); err != nil {
		cleanup()
		return fmt.Errorf("base %s: upload script: %w", name, err)
	}
	exit, err := be.Run(ctx, target, sandbox.ExecOpts{
		Cmd:    []string{"bash", "/tmp/pixels-setup.sh"},
		Stdout: out,
		Stderr: out,
		Root:   true,
	})
	if err != nil {
		cleanup()
		return fmt.Errorf("base %s: setup script: %w", name, err)
	}
	if exit != 0 {
		cleanup()
		return fmt.Errorf("base %s: setup script exited %d", name, exit)
	}

	// Stop and snapshot.
	if err := be.Stop(ctx, target); err != nil {
		cleanup()
		return fmt.Errorf("base %s: stop: %w", name, err)
	}
	if err := be.CreateSnapshot(ctx, target, InitialCheckpointLabel); err != nil {
		cleanup()
		return fmt.Errorf("base %s: snapshot: %w", name, err)
	}
	fmt.Fprintf(out, "==> Base %s ready (initial checkpoint created).\n", name)
	return nil
}

// loadSetupScript reads the setup script from disk or the embedded FS based
// on the path's `mcp:` prefix. Centralised so callers don't open-code.
func loadSetupScript(path string) ([]byte, error) {
	if strings.HasPrefix(path, "mcp:") {
		return DefaultsFS.ReadFile(strings.TrimPrefix(path, "mcp:"))
	}
	return os.ReadFile(path)
}

// latestCheckpointFor returns the most-recent (by CreatedAt) checkpoint on
// the named container, or ok=false if none exist.
func latestCheckpointFor(ctx context.Context, be sandbox.Sandbox, container string) (sandbox.Snapshot, bool, error) {
	snaps, err := be.ListSnapshots(ctx, container)
	if err != nil {
		return sandbox.Snapshot{}, false, err
	}
	if len(snaps) == 0 {
		return sandbox.Snapshot{}, false, nil
	}
	latest := snaps[0]
	for _, s := range snaps[1:] {
		if s.CreatedAt.After(latest.CreatedAt) {
			latest = s
		}
	}
	return latest, true, nil
}
```

(Add imports: `os`, `strings`, `time`, `github.com/deevus/pixels/internal/config`.)

**Step 5: Update `fakeSandbox` (test fake)**

In `internal/mcp/tools_test.go` (or wherever `fakeSandbox` lives), ensure it implements the methods used by `BuildBase`. Add structures for tracking:

```go
type fakeSandbox struct {
	// ... existing ...
	containers map[string]sandbox.Instance
	snapshots  map[string]time.Time // key "<container>:<label>" -> created at
	cloned     []cloneCall
	runs       [][]string
}

type cloneCall struct {
	source, dest string
}

func (f *fakeSandbox) CloneFrom(ctx context.Context, source, label, dest string) error {
	f.cloned = append(f.cloned, cloneCall{source, dest})
	if f.containers == nil { f.containers = make(map[string]sandbox.Instance) }
	f.containers[dest] = sandbox.Instance{Name: dest, Status: sandbox.StatusRunning}
	return nil
}

func (f *fakeSandbox) CreateSnapshot(ctx context.Context, name, label string) error {
	if f.snapshots == nil { f.snapshots = make(map[string]time.Time) }
	f.snapshots[name+":"+label] = time.Now()
	return nil
}

func (f *fakeSandbox) ListSnapshots(ctx context.Context, name string) ([]sandbox.Snapshot, error) {
	var out []sandbox.Snapshot
	prefix := name + ":"
	for k, t := range f.snapshots {
		if strings.HasPrefix(k, prefix) {
			out = append(out, sandbox.Snapshot{Label: strings.TrimPrefix(k, prefix), CreatedAt: t})
		}
	}
	return out, nil
}

func (f *fakeSandbox) Run(ctx context.Context, name string, opts sandbox.ExecOpts) (int, error) {
	f.runs = append(f.runs, opts.Cmd)
	return 0, nil
}
```

(Adapt to whatever the existing fake uses; add only what's missing.)

**Step 6: Run tests**

```
go test ./internal/mcp -run TestBuildBase -race -v
```

Expected: PASS.

**Step 7: Commit**

```bash
git add internal/mcp/buildbase.go internal/mcp/buildbase_test.go internal/mcp/tools_test.go
git commit -m "feat(mcp): rewrite BuildBase for from/parent_image + initial checkpoint"
```

---

## Task 9: Rewrite `provisionFromBase` and add `BuildChain` cascade resolver

**Files:**
- Create: `internal/mcp/cascade.go`
- Create: `internal/mcp/cascade_test.go`
- Modify: `internal/mcp/tools.go` (`provisionFromBase`)
- Modify: `internal/mcp/tools_test.go`

**Step 1: Write the failing tests for cascade**

Create `internal/mcp/cascade_test.go`:

```go
package mcp

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/deevus/pixels/internal/config"
)

func TestBuildChainBuildsBottomUp(t *testing.T) {
	cfg := &config.Config{
		MCP: config.MCP{
			BasePrefix: "px-base-",
			Bases: map[string]config.Base{
				"dev":    {ParentImage: "images:ubuntu/24.04", SetupScript: "mcp:bases/dev.sh"},
				"python": {From: "dev", SetupScript: "mcp:bases/python.sh"},
			},
		},
	}

	var built []string
	exists := func(container string) bool { return false }
	build := func(name string) error {
		built = append(built, name)
		return nil
	}

	if err := BuildChain(context.Background(), cfg, "python", exists, build); err != nil {
		t.Fatalf("BuildChain: %v", err)
	}
	if want := []string{"dev", "python"}; !equalStrings(built, want) {
		t.Errorf("built = %v, want %v", built, want)
	}
}

func TestBuildChainSkipsExistingLinks(t *testing.T) {
	cfg := &config.Config{
		MCP: config.MCP{
			BasePrefix: "px-base-",
			Bases: map[string]config.Base{
				"dev":    {ParentImage: "images:ubuntu/24.04", SetupScript: "mcp:bases/dev.sh"},
				"python": {From: "dev", SetupScript: "mcp:bases/python.sh"},
			},
		},
	}

	var built []string
	exists := func(container string) bool { return container == "px-base-dev" }
	build := func(name string) error {
		built = append(built, name)
		return nil
	}
	if err := BuildChain(context.Background(), cfg, "python", exists, build); err != nil {
		t.Fatalf("BuildChain: %v", err)
	}
	if want := []string{"python"}; !equalStrings(built, want) {
		t.Errorf("built = %v, want %v", built, want)
	}
}

func TestBuildChainErrorsOnUnknownTarget(t *testing.T) {
	cfg := &config.Config{MCP: config.MCP{Bases: map[string]config.Base{}}}
	err := BuildChain(context.Background(), cfg, "ghost", func(string) bool { return false }, func(string) error { return nil })
	if err == nil {
		t.Fatal("expected error for unknown target base")
	}
}

func TestBuildChainShortCircuitsOnBuildError(t *testing.T) {
	cfg := &config.Config{
		MCP: config.MCP{
			BasePrefix: "px-base-",
			Bases: map[string]config.Base{
				"dev":    {ParentImage: "images:ubuntu/24.04", SetupScript: "mcp:bases/dev.sh"},
				"python": {From: "dev", SetupScript: "mcp:bases/python.sh"},
			},
		},
	}
	wantErr := errors.New("nope")
	build := func(name string) error {
		if name == "dev" {
			return wantErr
		}
		t.Fatalf("python should not have been attempted after dev failed")
		return nil
	}
	err := BuildChain(context.Background(), cfg, "python", func(string) bool { return false }, build)
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want wraps %v", err, wantErr)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) { return false }
	for i := range a { if a[i] != b[i] { return false } }
	return true
}
```

**Step 2: Run to verify failure**

```
go test ./internal/mcp -run TestBuildChain -v
```

Expected: FAIL — `BuildChain` undefined.

**Step 3: Implement `cascade.go`**

```go
package mcp

import (
	"context"
	"fmt"

	"github.com/deevus/pixels/internal/config"
)

// BuildChain ensures the dependency chain for `target` is built. It walks
// the `from` chain bottom-up; for any link whose container does not exist
// (per `exists`), `build(name)` is invoked. Build failures short-circuit
// (children of a failed parent are not attempted).
//
// `exists(container)` is called with the fully-prefixed container name
// (BaseName(cfg, n)). `build(name)` is called with the bare base name.
func BuildChain(ctx context.Context, cfg *config.Config, target string, exists func(container string) bool, build func(name string) error) error {
	bases := cfg.MCP.Bases
	if _, ok := bases[target]; !ok {
		return fmt.Errorf("base %q is not declared in config", target)
	}

	// Walk from-chain to root, collecting names in build order (root first).
	var order []string
	seen := map[string]bool{}
	cur := target
	for {
		if seen[cur] {
			// Should never happen — config validation rejects cycles. Guard anyway.
			return fmt.Errorf("base %q: cycle detected during chain walk", cur)
		}
		seen[cur] = true
		order = append([]string{cur}, order...)
		b, ok := bases[cur]
		if !ok {
			return fmt.Errorf("base %q: missing from config (config validation should have caught this)", cur)
		}
		if b.From == "" {
			break
		}
		cur = b.From
	}

	for _, name := range order {
		if exists(BaseName(cfg, name)) {
			continue
		}
		if err := build(name); err != nil {
			return fmt.Errorf("build %s: %w", name, err)
		}
	}
	return nil
}

// (sort import to silence test imports if used elsewhere)
var _ = func() { var _ sort.Interface = nil }
```

(Drop the `sort` import shim if not needed.)

**Step 4: Update `provisionFromBase` in `internal/mcp/tools.go`**

Replace the body with:

```go
func (t *Tools) provisionFromBase(ctx context.Context, name string, in CreateSandboxIn) {
	baseCfg, ok := t.Cfg.MCP.Bases[in.Base]
	if !ok {
		t.State.MarkFailed(name, fmt.Errorf("base %q not declared in config", in.Base))
		_ = t.persist()
		return
	}
	_ = baseCfg // referenced via cfg.MCP.Bases inside BuildChain

	// Cascade build any missing links in the from-chain.
	exists := func(container string) bool {
		_, err := t.Backend.Get(ctx, container)
		return err == nil
	}
	build := func(baseName string) error {
		return t.Builder.Build(ctx, baseName)
	}
	if err := BuildChain(ctx, t.Cfg, in.Base, exists, build); err != nil {
		t.State.MarkFailed(name, err)
		_ = t.persist()
		return
	}

	// All chain links are present. Look up the latest checkpoint on the target base.
	target := BaseName(t.Cfg, in.Base)
	latest, ok2, err := latestCheckpointFor(ctx, t.Backend, target)
	if err != nil {
		t.State.MarkFailed(name, fmt.Errorf("list checkpoints on %s: %w", target, err))
		_ = t.persist()
		return
	}
	if !ok2 {
		t.State.MarkFailed(name, fmt.Errorf("base %s has no checkpoints; run `pixels checkpoint create %s`", in.Base, target))
		_ = t.persist()
		return
	}

	// Clone the sandbox.
	if err := t.Backend.CloneFrom(ctx, target, latest.Label, name); err != nil {
		t.State.MarkFailed(name, fmt.Errorf("clone: %w", err))
		_ = t.persist()
		return
	}
	if err := t.Backend.Ready(ctx, name, 2*time.Minute); err != nil {
		t.State.MarkFailed(name, fmt.Errorf("ready: %w", err))
		_ = t.persist()
		return
	}

	if inst, err := t.Backend.Get(ctx, name); err == nil && len(inst.Addresses) > 0 {
		t.State.SetIP(name, inst.Addresses[0])
	}
	t.State.MarkRunning(name)
	t.State.BumpActivity(name, time.Now().UTC())
	_ = t.persist()
}
```

Wire `Builder.DoBuild` (in `cmd/mcp.go`) so it knows how to build any base by name:

```go
builder.DoBuild = func(ctx context.Context, baseName string) error {
	bl, err := mcppkg.AcquireBuildLock(buildLockDir, baseName)
	if err != nil {
		return err
	}
	defer bl.Release()
	baseCfg, ok := cfg.MCP.Bases[baseName]
	if !ok {
		return fmt.Errorf("base %q not declared", baseName)
	}
	return mcppkg.BuildBase(ctx, sb, cfg, baseName, baseCfg, os.Stderr)
}
```

(BuildBase signature gained a `cfg` parameter in Task 8 — `cmd/mcp.go` must pass it now.)

**Step 5: Run tests**

```
go test ./internal/mcp -race -v
go build ./...
```

Expected: PASS, build clean.

**Step 6: Commit**

```bash
git add internal/mcp/cascade.go internal/mcp/cascade_test.go internal/mcp/tools.go cmd/mcp.go
git commit -m "feat(mcp): cascade-build chain + latest-checkpoint clone in provisionFromBase"
```

---

## Task 10: Update `list_bases` MCP tool to surface `from` and `last_checkpoint`

**Files:**
- Modify: `internal/mcp/tools.go` (`BaseView`, `ListBases`)
- Modify: `internal/mcp/tools_test.go`

**Step 1: Write the failing test**

```go
func TestListBasesIncludesFromAndLastCheckpoint(t *testing.T) {
	tt, fb := newTestTools(t)
	tt.Cfg = &config.Config{
		MCP: config.MCP{
			BasePrefix: "px-base-",
			Bases: map[string]config.Base{
				"dev":    {ParentImage: "images:ubuntu/24.04", Description: "dev"},
				"python": {From: "dev", Description: "python"},
			},
		},
	}

	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	fb.containers = map[string]sandbox.Instance{
		"px-base-dev":    {Name: "px-base-dev", Status: sandbox.StatusStopped},
		"px-base-python": {Name: "px-base-python", Status: sandbox.StatusStopped},
	}
	fb.snapshots = map[string]time.Time{
		"px-base-dev:initial":     now.Add(-2 * time.Hour),
		"px-base-python:initial":  now.Add(-1 * time.Hour),
		"px-base-python:custom":   now,
	}

	out, err := tt.ListBases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]BaseView{}
	for _, b := range out.Bases {
		byName[b.Name] = b
	}

	py := byName["python"]
	if py.From != "dev" {
		t.Errorf("python.From = %q, want dev", py.From)
	}
	if py.LastCheckpoint == nil || !py.LastCheckpoint.Equal(now) {
		t.Errorf("python.LastCheckpoint = %v, want %v", py.LastCheckpoint, now)
	}
	if py.Status != "ready" {
		t.Errorf("python.Status = %q, want ready", py.Status)
	}

	dev := byName["dev"]
	if dev.From != "" {
		t.Errorf("dev.From should be empty, got %q", dev.From)
	}
	if dev.ParentImage != "images:ubuntu/24.04" {
		t.Errorf("dev.ParentImage = %q", dev.ParentImage)
	}
}
```

**Step 2: Run to verify failure**

```
go test ./internal/mcp -run TestListBasesIncludesFromAndLastCheckpoint -v
```

Expected: FAIL.

**Step 3: Implement**

In `internal/mcp/tools.go`:

```go
type BaseView struct {
	Name           string     `json:"name"`
	Description    string     `json:"description,omitempty"`
	ParentImage    string     `json:"parent_image,omitempty"`
	From           string     `json:"from,omitempty"`
	Status         string     `json:"status"` // "ready" | "missing" | "building" | "failed"
	Error          string     `json:"error,omitempty"`
	LastCheckpoint *time.Time `json:"last_checkpoint,omitempty"`
}
```

Replace `ListBases`:

```go
func (t *Tools) ListBases(ctx context.Context) (ListBasesOut, error) {
	if t.Cfg == nil {
		return ListBasesOut{}, nil
	}
	out := make([]BaseView, 0, len(t.Cfg.MCP.Bases))
	for name, b := range t.Cfg.MCP.Bases {
		v := BaseView{
			Name:        name,
			Description: b.Description,
			ParentImage: b.ParentImage,
			From:        b.From,
		}
		// In-flight or recently failed?
		if t.Builder != nil {
			if status, err := t.Builder.Status(name); status != "" {
				v.Status = status
				if err != nil {
					v.Error = err.Error()
				}
				out = append(out, v)
				continue
			}
		}
		// Container existence + checkpoint timestamp.
		container := BaseName(t.Cfg, name)
		if _, err := t.Backend.Get(ctx, container); err != nil {
			if errors.Is(sandbox.WrapNotFound(err), sandbox.ErrNotFound) {
				v.Status = "missing"
				out = append(out, v)
				continue
			}
			v.Status = "failed"
			v.Error = err.Error()
			out = append(out, v)
			continue
		}
		v.Status = "ready"
		if latest, ok, err := latestCheckpointFor(ctx, t.Backend, container); err == nil && ok {
			t := latest.CreatedAt
			v.LastCheckpoint = &t
		}
		out = append(out, v)
	}
	return ListBasesOut{Bases: out}, nil
}
```

**Step 4: Run tests**

```
go test ./internal/mcp -race -v
```

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/mcp/tools.go internal/mcp/tools_test.go
git commit -m "feat(mcp): surface from and last_checkpoint on list_bases"
```

---

## Task 11: New `cmd/base.go` with `pixels base list` and `pixels base build`

**Files:**
- Create: `cmd/base.go`
- Modify: `cmd/mcp_base.go` (delete file, the existing `pixels mcp build-base/...` commands move out)
- Modify: `cmd/mcp.go` (drop `mcpCmd.AddCommand` calls for the moved subcommands if any reference them)

**Step 1: Implement `cmd/base.go`**

```go
package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/deevus/pixels/internal/config"
	mcppkg "github.com/deevus/pixels/internal/mcp"
	"github.com/spf13/cobra"
)

var baseCmd = &cobra.Command{
	Use:   "base",
	Short: "Manage pixel bases (templates that sandboxes clone from)",
}

var baseListCmd = &cobra.Command{
	Use:   "list",
	Short: "List declared bases and their status",
	RunE:  runBaseList,
}

var baseBuildCmd = &cobra.Command{
	Use:   "build <name>",
	Short: "Build a base from its setup script (cascade-builds dependencies if missing)",
	Args:  cobra.ExactArgs(1),
	RunE:  runBaseBuild,
}

func init() {
	baseCmd.AddCommand(baseListCmd)
	baseCmd.AddCommand(baseBuildCmd)
	rootCmd.AddCommand(baseCmd)
}

func runBaseList(cmd *cobra.Command, args []string) error {
	cfg, ok := cmd.Context().Value(configKey).(*config.Config)
	if !ok {
		return fmt.Errorf("config not loaded")
	}
	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	w := newTabWriter(cmd)
	defer w.Flush()
	fmt.Fprintln(w, "NAME\tFROM/IMAGE\tSTATUS\tLAST_CHECKPOINT\tDESCRIPTION")

	for name, b := range cfg.MCP.Bases {
		container := mcppkg.BaseName(cfg, name)
		fromOrImage := b.ParentImage
		if b.From != "" {
			fromOrImage = "from:" + b.From
		}

		var status, lastChk string
		if _, err := sb.Get(context.Background(), container); err != nil {
			status = "missing"
		} else {
			status = "ready"
			if latest, ok, err := mcppkgLatestCheckpoint(context.Background(), sb, container); err == nil && ok {
				lastChk = latest.CreatedAt.UTC().Format("2006-01-02 15:04:05Z")
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", name, fromOrImage, status, lastChk, b.Description)
	}
	return nil
}

func runBaseBuild(cmd *cobra.Command, args []string) error {
	name := args[0]
	cfg, ok := cmd.Context().Value(configKey).(*config.Config)
	if !ok {
		return fmt.Errorf("config not loaded")
	}
	if _, ok := cfg.MCP.Bases[name]; !ok {
		return fmt.Errorf("base %q not declared in config", name)
	}
	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	exists := func(container string) bool {
		_, err := sb.Get(context.Background(), container)
		return err == nil
	}
	build := func(baseName string) error {
		baseCfg := cfg.MCP.Bases[baseName]
		// File-lock per base name across CLI vs daemon.
		bl, err := mcppkg.AcquireBuildLock(cfg.MCPStateFile()+".d", baseName)
		if err != nil {
			return err
		}
		defer bl.Release()
		return mcppkg.BuildBase(context.Background(), sb, cfg, baseName, baseCfg, os.Stderr)
	}
	return mcppkg.BuildChain(context.Background(), cfg, name, exists, build)
}

// mcppkgLatestCheckpoint exposes the same lookup the daemon does; CLI uses it for `list`.
func mcppkgLatestCheckpoint(ctx context.Context, sb interface {
	ListSnapshots(ctx context.Context, name string) ([]sandboxSnapshot, error)
}, name string) (sandboxSnapshot, bool, error) {
	// Stub — replaced by direct call to the package helper.
	return sandboxSnapshot{}, false, nil
}
```

> **Engineer note:** The `mcppkgLatestCheckpoint` shim above is a placeholder so the file compiles in isolation. In practice, expose `LatestCheckpointFor` (capitalised) from `internal/mcp/buildbase.go` so `cmd/base.go` can call it directly. Update Task 8's helper to be exported (`LatestCheckpointFor`) and update its callers in `provisionFromBase`. Then `cmd/base.go` calls `mcppkg.LatestCheckpointFor(ctx, sb, container)` — no shim needed.

**Step 2: Delete the old `cmd/mcp_base.go`**

```bash
git rm cmd/mcp_base.go
```

**Step 3: Run tests + smoke**

```
go build -o pixels .
./pixels base --help
./pixels base list --help
./pixels base build --help
go test ./... -race
```

Expected: PASS, help text shows the two subcommands.

**Step 4: Commit**

```bash
git add cmd/base.go internal/mcp/buildbase.go internal/mcp/tools.go
git rm cmd/mcp_base.go
git commit -m "feat(cmd): add 'pixels base list/build' (replaces 'pixels mcp build-base')"
```

---

## Task 12: README + design-doc note about the new model

**Files:**
- Modify: `README.md`

**Step 1: Update the MCP server section**

In `README.md`, replace the existing "Base pixels" subsection (or add one if missing) with:

```markdown
### Base pixels

A base is a container that sandboxes clone from. Bases are declared in
config (or shipped as defaults) and built on demand.

Three bases ship out of the box:

- `dev` — Ubuntu 24.04 + git, curl, wget, jq, vim, build-essential
- `python` — `dev` + python3, pip, pipx, venv
- `node` — `dev` + Node 22 LTS, npm

Add your own in config:

    [mcp.bases.rust]
    parent_image = "images:ubuntu/24.04"      # or `from = "dev"`
    setup_script = "~/.config/pixels/bases/rust.sh"
    description  = "Rust toolchain"

Bases form a DAG via `from`. Cycle / missing-dep / both-set / neither-set
are rejected at config load.

Customise a base by mutating its container:

    pixels start px-base-python
    pixels exec px-base-python -- apt install vim
    pixels stop px-base-python
    pixels checkpoint create px-base-python

The next `create_sandbox(base="python")` call clones from the new
checkpoint. Existing sandboxes are unaffected (independent containers).

**Mutation-propagation gotcha.** Changes to `dev` do NOT auto-flow into
`python` or `node`. Both are independent containers built when `dev` was
in its prior state. To propagate: `pixels destroy px-base-python &&
pixels base build python`. Same semantics as Docker layered images.

CLI:

| Command | Action |
|---|---|
| `pixels base list` | Show declared bases + status |
| `pixels base build <name>` | Build the base; cascade-builds missing deps |
| `pixels destroy px-base-<name>` | Delete a base (existing CLI) |
| `pixels checkpoint create px-base-<name>` | Publish a new state for clones |
```

**Step 2: Commit**

```bash
git add README.md
git commit -m "docs: document base pixels (containers as bases, two new CLI verbs)"
```

---

## Final cleanup

**Step 1: Run the full matrix**

```
go test ./... -race -count=1
go build ./...
go vet ./...
```

All clean.

**Step 2: Confirm leftover-marker check**

```
grep -rn "TODO\|FIXME\|placeholder\|panic(\"not implemented\")" internal/mcp cmd/base.go
```

Expected: no surprises (only intentional notes if any).

**Step 3: Smoke-test against a live backend**

```
pixels mcp --verbose
# in another terminal:
pixels base list
pixels base build dev
pixels base list
# verify status flips from missing to ready, last_checkpoint populated
pixels start px-base-dev
pixels exec px-base-dev -- apt install -y zoxide
pixels stop px-base-dev
pixels checkpoint create px-base-dev
pixels base build python
# verify python clone has zoxide via pixels exec px-base-python -- which zoxide
```

**Step 4: Final commit only if anything changed**

```bash
git commit -am "chore(mcp): final bases-as-containers cleanup"
```

---

## Out of scope for this plan

- Imperative base creation (auto-discover any `<base_prefix>*` container as a base). Add later if there's demand.
- Layered scripts within a single base (multiple `setup_scripts` per base). Inheritance covers it.
- Drift detection on setup-script changes.
- Customisation wrapper (`pixels base modify` / `commit`). Existing CLI is enough.
- Versioned publish vs safety checkpoints. Latest-by-time is the v1 trade-off.

If you find yourself wanting one of these, stop and check with the user.
