# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

`pixels` is a Go CLI tool for managing disposable Linux containers (Incus) on TrueNAS via WebSocket API. It handles the full lifecycle: create, start, stop, destroy, SSH console/exec, and ZFS snapshot-based checkpoints.

## Build & Test

```bash
go build              # produces ./pixels binary
go test ./...         # run all tests
go test ./internal/config  # run tests for a single package
```

No Makefile, no linter configured. Standard `go build` / `go test`.

## Architecture

```
main.go          → cmd.Execute()
cmd/             → Cobra commands (create, start, stop, destroy, list, console, exec, checkpoint)
  root.go        → PersistentPreRunE loads config; shared helpers (containerName, displayName, resolveIP)
internal/
  config/        → TOML config from ~/.config/pixels/config.toml, env var overrides (PIXELS_*), CLI flag overrides
  cache/         → JSON disk cache in ~/.cache/pixels/ for container IP/status (avoids API round-trips)
  truenas/       → WebSocket client wrapping truenas-go; provisioning (SSH keys, DNS, rc.local injection)
  dataset/       → ZFS dataset path resolution for checkpoint commands
  ssh/           → WaitReady (TCP poll), Console (syscall.Exec replaces process), Exec (run remote command)
```

**Key patterns:**
- All containers are prefixed `px-` (hardcoded in `root.go`). Commands accept bare names and prepend the prefix.
- Config priority: TOML file < `PIXELS_*` env vars < CLI flags (`--host`, `--api-key`, `-u`).
- Cache is populated after `create`/`start`/`checkpoint restore` and evicted on `stop`/`destroy`.
- `console` and `exec` check the local cache first (fast path), falling back to the TrueNAS API.
- Destroy retries 3 times with 2s delays to handle Incus storage release timing.
- NIC parent and type are auto-detected from the host's gateway interface when not configured explicitly.
- `truenas` package fields use interfaces (`VirtServiceAPI`, `InterfaceServiceAPI`, etc.) for testability with library-provided mocks.

## Conventions

- Commit messages: `type: description` (e.g., `feat:`, `fix:`, `perf:`).
- Standard Go error handling with `fmt.Errorf` wrapping for context.
- Table-driven tests in `_test.go` files.
- Remote is a self-hosted Forgejo instance.
