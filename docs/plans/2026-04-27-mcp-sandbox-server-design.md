# MCP Sandbox Server for Pixels

**Date:** 2026-04-27
**Status:** Design
**Topic:** Expose pixels container lifecycle as an MCP server so AI agents can use Incus containers as ephemeral code sandboxes.

## Goal

Let an AI agent create disposable Linux sandboxes on demand, write and run code in them, and discard them — without the agent having to shell out to the `pixels` CLI or learn the WebSocket API.

The agent drives the full lifecycle (create, exec, file I/O, destroy). A TTL reaper handles forgotten sandboxes: idle ones stop, abandoned ones are eventually destroyed.

## Non-goals

- Persistent shared dev environments (use the regular `pixels` CLI).
- Interactive console sessions (agents don't need a TTY).
- Checkpoint/restore over MCP (out of scope for v1; can be added later).
- Network policy management over MCP (use `pixels network`).

## Architecture

```
pixels mcp                            # new cobra subcommand, stdio transport
├── starts background reaper goroutine
├── loads ~/.cache/pixels/mcp-state.json (or creates)
├── on startup: reap anything past TTL
└── handles MCP requests over stdin/stdout

internal/mcp/                         # new package
├── server.go      — MCP protocol handlers, tool dispatch
├── tools.go       — tool definitions + JSON schemas
├── state.go       — state file load/save, last-activity tracking
├── reaper.go      — TTL enforcement loop
└── files.go       — write_file/read_file/list_files via SFTP

reuses:
internal/config    — extended with [mcp] section
internal/truenas   — create/start/stop/destroy unchanged
internal/ssh       — Exec already works; SFTP added (golang.org/x/crypto/ssh/sftp)
internal/retry     — wraps WaitReady for newly-created sandboxes
```

The MCP server is a thin facade. No business logic moves out of existing
packages — `internal/mcp` maps tool calls onto the same code paths the CLI
uses.

The reaper is a single goroutine ticking on `mcp.reap_interval` (default
1 min). Each tick:

1. Reload state file.
2. For every sandbox: if `now - last_activity_at > idle_stop_after` and
   status is `running`, call `truenas.Stop()` and update status.
3. If `now - created_at > hard_destroy_after`, call `truenas.Destroy()`
   and remove the entry.
4. Save state file.

State file (`~/.cache/pixels/mcp-state.json`):

```json
{
  "sandboxes": [
    {
      "name": "px-mcp-abc123",
      "label": "experiment-1",
      "image": "ubuntu/24.04",
      "ip": "10.0.0.42",
      "status": "running",
      "created_at": "2026-04-27T10:00:00Z",
      "last_activity_at": "2026-04-27T10:15:00Z"
    }
  ]
}
```

## MCP tool inventory

| Tool | Args | Returns |
|---|---|---|
| `create_sandbox` | `label?`, `image?` | `{name, ip, status}` |
| `list_sandboxes` | — | `[{name, label, status, created_at, last_activity_at, idle_for}]` |
| `destroy_sandbox` | `name` | `{ok}` |
| `start_sandbox` | `name` | `{name, ip, status}` |
| `stop_sandbox` | `name` | `{ok}` |
| `exec` | `name`, `command[]`, `cwd?`, `env?`, `timeout_sec?` | `{exit_code, stdout, stderr}` |
| `write_file` | `name`, `path`, `content`, `mode?` | `{ok, bytes_written}` |
| `read_file` | `name`, `path`, `max_bytes?` | `{content, truncated}` |
| `list_files` | `name`, `path`, `recursive?` | `[{path, size, mode, is_dir}]` |

Notes:

- `create_sandbox` auto-generates a name as `<prefix><6-char-suffix>`,
  where `<prefix>` comes from `mcp.prefix` config (default `px-mcp-`).
  `label` is optional metadata for `list_sandboxes` output to help the
  agent disambiguate.
- `image` defaults to `mcp.default_image`. Per-call override allowed.
- `exec`, `write_file`, `read_file`, `list_files` all bump
  `last_activity_at` on success.
- `exec` enforces a hard timeout cap from `mcp.exec_timeout_max` to
  prevent runaway commands. Agent-specified `timeout_sec` is clamped.
- `start_sandbox` exists so the agent can revive a sandbox the reaper
  stopped (within the `hard_destroy_after` window).

## Configuration

New `[mcp]` section in `~/.config/pixels/config.toml`:

```toml
[mcp]
prefix = "px-mcp-"               # name prefix for MCP-created sandboxes
default_image = "ubuntu/24.04"   # agent can override per create
idle_stop_after = "1h"           # stop sandbox after this much idle time
hard_destroy_after = "24h"       # destroy sandbox after this much wall-clock time
reap_interval = "1m"             # how often the reaper ticks
state_file = ""                  # default: ~/.cache/pixels/mcp-state.json
exec_timeout_max = "10m"         # hard cap on exec timeouts
```

Env-var overrides via `PIXELS_MCP_*` to match the existing pattern
(`PIXELS_MCP_PREFIX`, `PIXELS_MCP_IDLE_STOP_AFTER`, etc.).

CLI flag overrides on the `pixels mcp` subcommand are not added in v1 —
MCP is configured once and started by the agent host.

## Concurrency

- Per-sandbox mutex serializes `exec` / `write_file` / `read_file` /
  `list_files` to avoid SSH session races. Different sandboxes run in
  parallel.
- Reaper takes a global state lock when reading/writing state file;
  tool handlers take it briefly to update `last_activity_at`. No
  long-held locks during SSH I/O.

## Error handling

- TrueNAS unreachable → return MCP error to agent; server stays up.
- Sandbox destroyed externally (e.g. via `pixels destroy`) → next
  reaper tick prunes the orphan from state.
- Container not ready after create → `WaitReady` with timeout from
  existing config; surface as MCP error if exceeded.
- State file corrupt → log warning, treat as empty, continue. Orphan
  sandboxes need manual cleanup via `pixels list` + `pixels destroy`.
- SFTP transfer of large files → `read_file` honors `max_bytes` and
  reports `truncated: true`. `write_file` has no enforced size cap in
  v1 (large writes fail naturally on disk).

## Testing

- `internal/mcp/state_test.go` — load/save/prune, malformed JSON,
  concurrent updates.
- `internal/mcp/reaper_test.go` — table-driven, mocked clock, mocked
  truenas API (interface-based, matches the existing pattern in
  `internal/truenas`).
- `internal/mcp/files_test.go` — SFTP wrappers against the in-memory
  test server in `golang.org/x/crypto/ssh`.
- `internal/mcp/server_test.go` — tool dispatch, argument validation,
  JSON-schema correctness.
- No live TrueNAS required in CI. Manual smoke test instructions to be
  added to `README.md` once implemented.

## Out-of-scope / future work

- Checkpoint/restore exposed as MCP tools (snapshot a working
  environment, hand it to the next chat).
- A `pixels mcp doctor` subcommand that pings TrueNAS, checks SSH key,
  and prints a report.
- Per-sandbox network egress policy via MCP tool.
- A `read_file_chunk(name, path, offset, length)` tool for streaming
  large outputs without truncation.

## Open questions

None blocking v1.
