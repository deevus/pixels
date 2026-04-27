# Bases as Containers — Design

**Date:** 2026-04-27
**Status:** Design
**Topic:** Replace the snapshot-based base-pixel lifecycle with a container-as-base model. Ship embedded defaults so first-run is zero-config. Support base-from-base inheritance for shared layers (e.g. `python` extends `dev`).

## Problem

The current MCP base-pixel system requires the user to:
1. Write `[mcp.bases.<name>]` config entries.
2. Author shell scripts at the referenced `setup_script` paths.

That's a wall before the first `create_sandbox(base="X")` works. We want "jump in and go": fresh install → agent calls `create_sandbox(base="python")` → it just works.

The existing model also exposes a snapshot-clone abstraction (snapshot lives on a `px-base-builder-<name>` container, clones from `px-base-<name>` snapshot) which adds ceremony without user-visible value. A base is conceptually just "a container with stuff installed"; modeling it as such is simpler.

## Design

### Model

- **A base IS a container.** Named `<base_prefix><name>`, where `base_prefix` is configurable (default `px-base-`). No accompanying persistent snapshot.
- **A sandbox is a CLONE of a base.** `create_sandbox(base="X")` does `Backend.CopyInstance("<base_prefix>X", new-sandbox-name)`. Incus handles COW under the hood.
- **Customisation is mutation.** User starts the base container, runs commands, stops it. Future clones pick up current state automatically (next create_sandbox(base=X) sees the mutated container). Pre-existing sandboxes keep their original state (independent containers, basic COW).
- **No save command.** The container is always in its current state, and that current state IS the base.

### Embedded defaults

Three setup scripts shipped via `//go:embed`:

```
internal/mcp/bases/dev.sh        Ubuntu 24.04 + git, curl, wget, jq, vim,
                                 build-essential, ca-certificates
internal/mcp/bases/python.sh     dev + python3, pip, pipx, venv
internal/mcp/bases/node.sh       dev + Node 22 LTS, npm
```

`internal/mcp/defaults.go` declares:

```go
//go:embed bases/*.sh
var DefaultsFS embed.FS

var DefaultBases = map[string]config.Base{
    "dev":    {ParentImage: "images:ubuntu/24.04", SetupScript: "mcp:bases/dev.sh", Description: "..."},
    "python": {From: "dev", SetupScript: "mcp:bases/python.sh", Description: "..."},
    "node":   {From: "dev", SetupScript: "mcp:bases/node.sh", Description: "..."},
}
```

`internal/config/config.go` `Load()` merges defaults: any name not in the user's `[mcp.bases]` is added from `DefaultBases`. User config fully replaces a default on name conflict — no field merge.

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
2. For each missing link, walk into `Builder.Build(name)`:
   - `Builder` (existing) dedupes concurrent in-process callers.
   - `BuildLock` (existing) serialises across CLI vs daemon processes.
   - `BuildBase` (existing, modified): if base has `parent_image`, create new container from the image. If base has `from`, do `Backend.CopyInstance("<base_prefix><parent>", "<base_prefix><name>")`. Run the setup script in the new container. Leave it stopped. **No `CreateSnapshot` step at the end** — that's the change from current.
3. After the chain is built, clone the sandbox: `Backend.CopyInstance("<base_prefix>python", new-sandbox-name)`.

### Configuration

```toml
[mcp]
prefix      = "px-mcp-"     # sandbox name prefix (existing)
base_prefix = "px-base-"    # new — base container name prefix

# bases (existing) — supports `from` (new) as alternative to `parent_image`
[mcp.bases.<name>]
parent_image = "images:ubuntu/24.04"   # XOR with `from`
from         = "dev"                   # XOR with `parent_image`
setup_script = "mcp:bases/<name>.sh"   # `mcp:` scheme = embedded FS
description  = "..."
```

Env-var override: `PIXELS_MCP_BASE_PREFIX`.

### MCP tool surface

- `list_bases` → `[{name, from?, parent_image?, description, status, error?}]`. Status from container existence (`Backend.Get(name)` + `errors.Is(err, sandbox.ErrNotFound)`) and the in-memory `Builder.Status(name)`.
- `create_sandbox(base?, label?)` — unchanged. Cascade-builds the dep chain transparently if any link is missing.

### CLI

`pixels base` becomes a new top-level subcommand group:

| Command | Action |
|---|---|
| `pixels base list` | Show declared bases + status. Tabular: `NAME / FROM-OR-IMAGE / STATUS / DESCRIPTION`. |
| `pixels base build <name>` | Cascade-build the dep chain. Streams script output to stderr. |
| `pixels base delete <name>` | Destroy the base container. Refuses if any other base has `from = <name>`. Clones survive (independent). |

No `rebuild` command — that's `delete && build`.

Customisation is the existing CLI: `pixels start <base-name>`, `pixels exec <base-name> -- ...`, `pixels stop <base-name>`. Discoverability via README.

### Code changes

**Dies:**
- `Backend.SnapshotExists` interface method (just added) → replaced by `Backend.Get(name)` + `errors.Is(err, sandbox.ErrNotFound)`.
- `BuilderContainerName` / `SnapshotName` helpers → collapsed into one `BaseName(cfg, name)` that respects the configurable prefix.
- Persistent snapshot lifecycle in `BuildBase`: no more `CreateSnapshot` after the script runs.
- `Backend.CloneFrom(source, label, newName)` — repurposed to `Backend.CopyInstance(source, dest)` (drop the `label` param). Snapshot-based clone isn't used anywhere else right now.

**Survives:**
- `Builder` (singleflight + failure cache) — still dedupes concurrent first-time builds.
- `BuildLock` (flock) — still serialises CLI vs daemon during initial build.
- `BuildBase` — still creates the container from a script. Just stops calling `CreateSnapshot` and gains the `from`-handling branch.

**Moves:**
- `cmd/mcp_base.go` → `cmd/base.go`. Subcommand registration moves from `mcpCmd` to a new `baseCmd`.

### Edge cases

- **Mutation propagation gotcha.** If user mutates `dev` (`apt install zoxide`), changes do NOT auto-flow into `python` or `node`. Both were built when `dev` was in its prior state and are now their own containers. To propagate: `pixels base delete python && pixels base build python`. Same semantics as Docker layered images. Documented in README.
- **Failing dependency.** If `dev` build fails, `python` build short-circuits with the failure. `Builder` failure-cache (10-min TTL) applies to dev; further `create_sandbox(base="python")` calls return the cached dev failure immediately. Cache clears on successful retry.
- **Default replaced by user config.** User's `[mcp.bases.python]` fully wins — no field merge with embedded default.
- **Reaper exclusion.** Bases live forever (until user `pixels base delete`s). Reaper already ignores names matching the base prefix; this stays.

### Testing

- `internal/config` — `parent_image`/`from` mutual-exclusion, cycle detection, dep resolution, defaults merge.
- `internal/mcp` — fake-backend tests for cascade build, failure cascading, and the new `CopyInstance` clone path. Existing test fakes need a `CopyInstance` method.
- `sandbox/incus` + `sandbox/truenas` — backend-level smoke that `CopyInstance` produces an independent container with the expected initial state.
- Manual smoke in README: build python, verify `pixels base list` shows the chain, customise dev (`pixels start px-base-dev` → `apt install zoxide` → `pixels stop`), `pixels base delete python && pixels base build python`, confirm zoxide is now in python clones.

## Out of scope (v1)

- Imperative base creation (B from earlier brainstorm — `pixels create` then auto-discover `<base_prefix>*` containers as bases). Add later if there's demand.
- Layered scripts within a single base (multiple `setup_scripts` per base). Inheritance (`from`) covers the use case.
- Drift detection on setup-script changes after a base is built. Explicit YAGNI.
- Customisation wrapper (`pixels base customise <name>` that wraps start + exec + stop). Existing CLI is enough.
- Versioned base snapshots / history. Bases are mutable; clones survive independently.
