# Bases as Containers — Design

**Date:** 2026-04-27
**Status:** Implemented
**Topic:** Replace the snapshot-based base-pixel lifecycle with a container-as-base model. Ship embedded defaults so first-run is zero-config. Support base-from-base inheritance for shared layers (e.g. `python` extends `dev`).

## Problem

The current MCP base-pixel system requires the user to:
1. Write `[mcp.bases.<name>]` config entries.
2. Author shell scripts at the referenced `setup_script` paths.

That's a wall before the first `create_sandbox(base="X")` works. We want "jump in and go": fresh install → agent calls `create_sandbox(base="python")` → it just works.

The existing model also exposes a snapshot-clone abstraction (snapshot lives on a `px-base-builder-<name>` container, clones from `px-base-<name>` snapshot) which adds ceremony without user-visible value. A base is conceptually just "a container with stuff installed"; modeling it as such is simpler.

## Design

### Model

- **A base IS a container.** Named `<base_prefix><name>`, where `base_prefix` is configurable (default `px-base-`). Bases live forever (until explicitly deleted); the reaper ignores the prefix.
- **A sandbox is a CLONE of the base's most-recent checkpoint.** `create_sandbox(base="X")` finds the latest-by-time checkpoint on `<base_prefix>X` and clones from it via the existing `Backend.CloneFrom(name, label, newName)`.
- **Customisation reuses existing CLI.** User runs `pixels start <base>`, `pixels exec <base> -- ...`, `pixels stop <base>` to mutate. No new "modify" verb. `pixels checkpoint create <base>` publishes the current state — sandbox clones now see it. No new "commit" verb.
- **Initial checkpoint is automatic.** After `pixels base build` runs the setup script and stops the container, the daemon creates the first checkpoint so the base is publishable immediately. Without this, `create_sandbox(base="X")` would fail with "no checkpoints yet" right after build.

### Latest-by-time semantics

`Backend.ListSnapshots(name)` returns the checkpoints on a container with creation timestamps. Daemon picks the most recent and clones from it. **Trade-off accepted:** any `pixels checkpoint create <base>` advances the "what sandboxes clone from" pointer. If a user takes a checkpoint as a *safety rollback* before mutating, sandboxes clone from that safety net until the user takes another checkpoint after their changes. Documented as a quirk; the rule is "the most recent checkpoint wins, period."

This requires `sandbox.Snapshot` to expose a creation timestamp:

```go
type Snapshot struct {
    Label     string
    Size      int64
    CreatedAt time.Time   // NEW — backends populate from Incus / TrueNAS metadata
}
```

### Embedded defaults

Three setup scripts shipped via `//go:embed`:

```
internal/mcp/bases/dev.sh        Ubuntu 24.04 + git, curl, wget, jq, vim,
                                 build-essential, ca-certificates
internal/mcp/bases/python.sh     dev + python3, pip, pipx, venv
internal/mcp/bases/node.sh       dev + Node 22 LTS, npm
```

`internal/mcp/defaults.go`:

```go
//go:embed bases/*.sh
var DefaultsFS embed.FS

var DefaultBases = map[string]config.Base{
    "dev":    {ParentImage: "images:ubuntu/24.04", SetupScript: "mcp:bases/dev.sh", Description: "..."},
    "python": {From: "dev", SetupScript: "mcp:bases/python.sh", Description: "..."},
    "node":   {From: "dev", SetupScript: "mcp:bases/node.sh", Description: "..."},
}
```

`internal/config/config.go` `Load()` merges defaults: any name not in user's `[mcp.bases]` is added. User config fully replaces a default on name conflict — no field merge.

### Base inheritance (`from`)

A base config declares **exactly one** of `parent_image` or `from`:

```toml
[mcp.bases.dev]
parent_image = "images:ubuntu/24.04"
setup_script = "mcp:bases/dev.sh"
description  = "Ubuntu 24.04 + common dev tools"

[mcp.bases.python]
from         = "dev"
setup_script = "mcp:bases/python.sh"
description  = "dev + python3, pip, pipx"
```

Bases form a DAG. At config load:
- Walk every base's `from` chain. Cycle → fatal startup error with the cycle nodes named.
- `from = X` referencing a base not in config → fatal startup error.

### Build flow

When `create_sandbox(base="python")` runs and `<base_prefix>python` doesn't exist:

1. Resolve dep chain bottom-up: `python → dev → parent_image`.
2. For each missing link, `Builder.Build(name)`:
   - `Builder` (existing) dedupes concurrent in-process callers.
   - `BuildLock` (existing) serialises across CLI vs daemon processes.
   - `BuildBase` (modified): if base has `parent_image`, create new container from the image. If base has `from`, clone from the parent base's latest checkpoint into a new container. Run the setup script in the new container, stop it, then **create an initial checkpoint** so the base is publishable.
3. After the chain is built, daemon resolves `<base_prefix>python` and clones the sandbox from its latest checkpoint.

### Configuration

```toml
[mcp]
prefix      = "px-mcp-"     # sandbox name prefix (existing)
base_prefix = "px-base-"    # NEW — base container name prefix

[mcp.bases.<name>]
parent_image = "images:ubuntu/24.04"   # XOR with `from`
from         = "dev"                   # XOR with `parent_image`
setup_script = "mcp:bases/<name>.sh"   # `mcp:` scheme = embedded FS
description  = "..."
```

Env-var override: `PIXELS_MCP_BASE_PREFIX`.

### MCP tool surface

- `list_bases` → `[{name, from?, parent_image?, description, status, error?, last_checkpoint?}]`. Status from container existence (`Backend.Get(name)` + `errors.Is(err, sandbox.ErrNotFound)`) and the in-memory `Builder.Status(name)`. `last_checkpoint` is the timestamp of the most-recent checkpoint, surfaced so agents can tell if a base has unpublished mutations.
- `create_sandbox(base?, label?)` — unchanged surface; cascade-builds the dep chain transparently if any link is missing.

### CLI

`pixels base` becomes a new top-level subcommand group:

| Command | Action |
|---|---|
| `pixels base list` | Show declared bases + status, including the most-recent checkpoint timestamp. |
| `pixels base build <name>` | Cascade-build the dep chain. If any link in the chain is missing (e.g. `dev` was destroyed), it gets rebuilt from its config first. Stops at the end with the initial checkpoint created. |

**No `delete`, `commit`, or `modify` command.**

- **Deletion** uses existing `pixels destroy <base-container-name>`. If a user destroys `dev` while `python` exists, the python container still works (independent ZFS container from when it was built). Future `pixels base build python` triggers cascade-rebuild of dev automatically — no manual ordering required.
- **Customisation** uses existing `pixels start` / `pixels exec` / `pixels stop`.
- **Publishing** uses existing `pixels checkpoint create <base-container-name>`.

Discoverability via README. New CLI surface is two verbs total.

### Code changes

**Dies:**
- `Backend.SnapshotExists` interface method (just added) → replaced by `Backend.Get(name)` + `errors.Is(err, sandbox.ErrNotFound)`.
- `BuilderContainerName` / `SnapshotName` helpers → collapsed into `BaseName(cfg, name)`.
- The `px-base-builder-<name>` / `px-base-<name>` snapshot split → gone. Container is the base; checkpoints are checkpoints.

**Survives, mostly unchanged:**
- `Builder` (singleflight + failure cache) — still dedupes concurrent first-time builds.
- `BuildLock` (flock) — still serialises CLI vs daemon during initial build.
- `Backend.CloneFrom(source, label, newName)` — already takes a snapshot label; we pass the latest checkpoint's label.

**Modified:**
- `BuildBase`: drop the snapshot-with-magic-name step at the end; replace with a regular `Backend.CreateSnapshot(name, label)` for the initial checkpoint. Label can be `initial` or a timestamp; doesn't matter — daemon picks by time, not label.
- `sandbox.Snapshot`: add `CreatedAt time.Time`. Both backends update `ListSnapshots` to populate it.

**New:**
- `internal/mcp/bases/*.sh` — three real shell scripts.
- `internal/mcp/defaults.go` — `//go:embed` + `DefaultBases` map.
- `internal/config` — `Base.From string` field + cycle-detection / dep-resolution validation. `MCP.BasePrefix string`.
- `cmd/base.go` — three subcommands. Reuses `internal/mcp/builder.go` and `internal/mcp/buildbase.go` for shared build logic.

### Edge cases

- **No checkpoint yet but container exists.** Should not happen post-build (`BuildBase` always creates an initial checkpoint). If it happens (manual deletion), `create_sandbox(base="X")` fails with a clear error: "base X has no checkpoints — run `pixels checkpoint create <base_prefix>X` after publishing your changes."
- **Mutation propagation gotcha.** If user mutates `dev`, changes do NOT auto-flow into `python` or `node`. Both were built when `dev` was at its prior state. To propagate: `pixels destroy px-base-python && pixels base build python` (or just destroy; daemon cascade-rebuilds on next `create_sandbox`). Documented in README. Same semantics as Docker layered images.
- **Failing dependency.** If `dev` build fails, `python` cascade short-circuits with the dev failure. `Builder.failureTTL` (10-min) caches dev's failure; subsequent `create_sandbox(base="python")` calls return the cached failure immediately. Cache clears on successful retry.
- **Default replaced by user config.** User's `[mcp.bases.python]` fully wins — no field merge.
- **Reaper exclusion.** Bases live forever (until `pixels base delete`). Reaper already ignores names matching the configured base prefix; this stays.
- **Safety vs publish checkpoint conflation.** Any checkpoint on a base advances the clone source. Documented; users planning safety rollbacks need to checkpoint *after* mutations to re-publish.

### Testing

- `internal/config` — `parent_image`/`from` mutual-exclusion, cycle detection, dep resolution, defaults merge with user-config-wins semantics.
- `internal/mcp` — fake-backend tests for cascade build (parent built before child), failure cascading (dev fails → python fails fast), initial checkpoint creation by `BuildBase`, latest-by-time clone selection.
- `sandbox/incus` and `sandbox/truenas` — backend-level tests that `ListSnapshots` populates `CreatedAt` correctly. Smoke-only without a live backend; existing patterns apply.
- Manual smoke list in README: build python from scratch, verify `pixels base list` shows the chain + initial-checkpoint timestamp, customise dev (`pixels start px-base-dev` → `apt install zoxide` → `pixels stop` → `pixels checkpoint create px-base-dev`), `pixels base delete python && pixels base build python`, confirm new python clones include zoxide.

## Out of scope (v1)

- Imperative base creation (auto-discover any `<base_prefix>*` container as a base). Add later if there's demand.
- Layered scripts within a single base (multiple `setup_scripts` per base). Inheritance (`from`) covers the use case.
- Drift detection on setup-script changes after a base is built. Explicit YAGNI.
- Customisation wrapper (`pixels base modify` / `pixels base commit`). Existing CLI is enough.
- Versioned base "publish" (special label distinguishing publish from safety checkpoints). Latest-by-time accepted as the v1 trade-off.
- Promotion / cherry-pick of one base's checkpoint to another. Use `pixels checkpoint restore` if you need it.
