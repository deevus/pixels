# MCP Sandbox Server for Pixels

**Date:** 2026-04-27
**Status:** Design
**Topic:** Expose pixels container lifecycle as an MCP server so AI agents can use Incus containers as ephemeral code sandboxes. Single long-running daemon, streamable-HTTP transport.

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
pixels mcp                            # new cobra subcommand
├── acquires ~/.cache/pixels/mcp.pid (refuses to start if another instance is alive)
├── starts background reaper goroutine
├── loads ~/.cache/pixels/mcp-state.json (or creates)
├── on startup: reap anything past TTL
├── listens on http://127.0.0.1:<port>/mcp (streamable-HTTP transport)
└── on SIGTERM/SIGINT: flush state, release pidfile, exit

internal/mcp/                         # new package
├── server.go      — MCP protocol handlers, tool dispatch, streamable-HTTP wiring
├── tools.go       — tool definitions, lifecycle/exec/file CRUD/edit handlers
├── state.go       — state file load/save, last-activity tracking (in-memory + atomic write)
├── reaper.go      — TTL enforcement loop
└── pidfile.go     — single-instance pidfile lock

sandbox/                              # interface extension
├── sandbox.go     — adds Files capability (WriteFile/ReadFile/ListFiles/DeleteFile)
└── filesexec.go   — FilesViaExec helper: implements Files via shell over Exec

reuses:
internal/config           — extended with [mcp] section
sandbox/incus, truenas    — embed FilesViaExec to satisfy the new Files interface;
                            create/start/stop/delete/exec unchanged
```

The MCP server is a single long-running daemon. The user starts it once
(manually, via launchd/systemd, or however they prefer); all Claude Code
sessions on the host point at the same `http://127.0.0.1:<port>/mcp`
endpoint and share state. No business logic moves out of existing packages
— `internal/mcp` maps tool calls onto the same code paths the CLI uses.

**Single-instance enforcement.** On startup the daemon writes its PID to
`~/.cache/pixels/mcp.pid`. If the file already exists and the PID is alive,
startup fails with a clear error. If the PID is dead (stale pidfile), it is
overwritten. SIGTERM/SIGINT handler removes the pidfile cleanly. This is
the "C" option from the concurrency discussion: one daemon per host.

**Streamable-HTTP transport.** The daemon binds to `127.0.0.1` by default
(loopback only — no auth in v1). MCP clients connect via the standard
streamable-HTTP endpoint pattern (`POST /mcp` for requests, `GET /mcp` for
SSE notification stream, `Mcp-Session-Id` header for session correlation).
Multiple concurrent client sessions are fine — they share the in-memory
state under a `sync.RWMutex`.

The reaper is a single goroutine ticking on `mcp.reap_interval` (default
1 min). Each tick:

1. Snapshot in-memory state under read lock.
2. For every sandbox: if `now - last_activity_at > idle_stop_after` and
   status is `running`, call `Backend.Stop()` and update status.
3. If `now - created_at > hard_destroy_after`, call `Backend.Delete()`
   and remove the entry.
4. Persist state with atomic write (write to `mcp-state.json.tmp`,
   `os.Rename` to final path).

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
| `edit_file` | `name`, `path`, `old_string`, `new_string`, `replace_all?` | `{ok, replacements}` |
| `delete_file` | `name`, `path` | `{ok}` |
| `list_files` | `name`, `path`, `recursive?` | `[{path, size, mode, is_dir}]` |

`edit_file` mirrors Claude's Edit tool: errors if `old_string` is missing
or non-unique unless `replace_all=true`. Implemented in the MCP layer as
read-modify-write composition over the `Files` interface — no new backend
primitive needed. `mkdir`, `rename`, and recursive directory delete are
left out of v1; agents can do them via `exec`.

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
pid_file = ""                    # default: ~/.cache/pixels/mcp.pid
exec_timeout_max = "10m"         # hard cap on exec timeouts

# Transport
listen_addr = "127.0.0.1:8765"   # streamable-HTTP bind address; loopback by default
endpoint_path = "/mcp"           # streamable-HTTP endpoint path
```

Env-var overrides via `PIXELS_MCP_*` to match the existing pattern
(`PIXELS_MCP_PREFIX`, `PIXELS_MCP_LISTEN_ADDR`, etc.).

`pixels mcp` accepts CLI flag overrides for the most common knobs:
`--listen-addr`, `--state-file`, `--pid-file`. Useful for ad-hoc runs and
test isolation.

Client configuration (Claude Code MCP entry):

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

## Concurrency

Three dimensions:

1. **In-process (multiple HTTP request goroutines + reaper).** The state
   struct is guarded by a `sync.RWMutex`. Tool handlers take the write
   lock briefly to bump `last_activity_at` and to add/remove sandboxes;
   the reaper takes the read lock to snapshot, then write lock to commit
   pruning. Backend I/O (Exec/Files calls) happens *outside* the state
   lock — only a per-sandbox mutex (held just for the duration of the
   call) prevents concurrent shell sessions racing on the same container.
   Different sandboxes run in parallel.

2. **Crash mid-write.** State is persisted via atomic write: marshal,
   write to `<state_file>.tmp`, `os.Rename` to the final path. Atomic on
   the same filesystem; readers either see the old or the new file, never
   a partial.

3. **Multi-process (two `pixels mcp` daemons against the same state
   file).** Refused at startup. The pidfile pattern at
   `~/.cache/pixels/mcp.pid` is the gate: write PID with
   `O_CREAT|O_EXCL`; if the file exists and the recorded PID is alive,
   exit with a clear "another pixels mcp is running (pid=N)" message. If
   the PID is dead, the pidfile is treated as stale and overwritten. On
   clean exit (SIGTERM/SIGINT), the pidfile is removed.

The streamable-HTTP transport supports many concurrent client sessions
against the single daemon, so "single-process" does not mean
"single-client" — multiple Claude Code chats can share the same daemon
and see the same sandbox set via `list_sandboxes`.

## Error handling

- TrueNAS unreachable → return MCP error to agent; server stays up.
- Sandbox destroyed externally (e.g. via `pixels destroy`) → next
  reaper tick prunes the orphan from state.
- Container not ready after create → `WaitReady` with timeout from
  existing config; surface as MCP error if exceeded.
- State file corrupt → log warning, treat as empty, continue. Orphan
  sandboxes need manual cleanup via `pixels list` + `pixels destroy`.
- Large file reads → `read_file` honors `max_bytes` and reports
  `truncated: true` (detected by comparing stream bytes to `stat -c %s`).
  `write_file` has no enforced size cap in v1 (large writes fail
  naturally on disk).
- `edit_file` race conditions → the per-sandbox mutex covers the full
  read-modify-write so no concurrent `exec` can change the file between
  read and write.
- Pidfile collision on startup → exit non-zero with the live PID; do
  not start a second daemon.
- HTTP listen-address conflict → exit non-zero with the bind error.

## Security

- Default bind is `127.0.0.1` — loopback only, same trust boundary as
  the local user. No auth tokens in v1.
- If the user binds to a non-loopback interface (e.g. for remote use),
  v1 logs a warning at startup. Bearer-token auth is deferred to a
  future iteration.
- Sandboxes are full Linux containers with the same egress posture as
  the regular `pixels` CLI. The agent can run arbitrary code; that is
  the point. Egress controls remain a CLI concern (`pixels network`)
  in v1.

## Testing

- `internal/mcp/state_test.go` — load/save/prune, malformed JSON,
  atomic-write recovery (no `.tmp` left after success).
- `internal/mcp/pidfile_test.go` — live PID rejected, stale PID
  overwritten, Release removes the file.
- `internal/mcp/reaper_test.go` — table-driven, mocked clock, mocked
  lifecycle backend (interface defined locally to keep tests minimal).
- `internal/mcp/tools_test.go` — lifecycle handlers and edit/delete
  semantics against an in-memory `fakeSandbox` (write-read-edit-delete
  round-trip, edit uniqueness errors, replace_all).
- `internal/mcp/server_test.go` — smoke test that the streamable-HTTP
  handler mounts on the configured endpoint via `httptest.Server`.
- `sandbox/filesexec_test.go` — `FilesViaExec` shell composition with a
  fake `Exec` that captures Run/Output calls.
- No live TrueNAS or Incus required in CI. Manual smoke test
  instructions to be added to `README.md` once implemented.

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
