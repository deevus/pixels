# pixels

[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
[![GitHub release](https://img.shields.io/github/v/release/deevus/pixels)](https://github.com/deevus/pixels/releases)
[![Go](https://img.shields.io/github/go-mod/go-version/deevus/pixels)](go.mod)
[![Go Report Card](https://goreportcard.com/badge/github.com/deevus/pixels)](https://goreportcard.com/report/github.com/deevus/pixels)
[![Commercial Support](https://img.shields.io/badge/support-available-brightgreen)](#support)

Disposable Linux containers for AI coding agents.

Spin up sandboxed Linux containers pre-loaded with AI coding tools (Claude Code, Codex, OpenCode via mise). Each container gets snapshot-based checkpoints and network egress policies that control what the agent can reach. Ships with two backends: **Incus** (default, connects directly to a local or remote Incus daemon) and **TrueNAS** (manages Incus containers on TrueNAS SCALE via its WebSocket API).

## Features

- **Extensible backends** -- Incus native (default) or TrueNAS-managed, selected via config
- **Container lifecycle** -- create, start, stop, destroy, and list Incus containers
- **Console and exec** -- interactive console and remote command execution via native Incus API or SSH (backend-dependent)
- **Checkpoints** -- snapshot, restore, delete, and clone containers from checkpoints
- **AI agent provisioning** -- automatically installs mise, Claude Code, Codex, and OpenCode
- **Network egress policies** -- restrict outbound traffic to AI APIs, package registries, and Git (or a custom allowlist)
- **Configuration** -- TOML config file, `PIXELS_*` environment variables, and CLI flags
- **Network accessible** -- each container gets its own IP, reachable from the host for running and accessing services

## Prerequisites

- Go 1.25+ (for building from source)

**Incus backend (default):**
- [Incus](https://linuxcontainers.org/incus/) installed locally, or a remote Incus daemon accessible over HTTPS

**TrueNAS backend:**
- [TrueNAS SCALE](https://www.truenas.com/truenas-scale/) with Incus virtualization enabled
- TrueNAS API key (create one in the TrueNAS web UI under Credentials > API Keys)
- SSH key pair (defaults to `~/.ssh/id_ed25519`)

## Installation

```bash
mise use -g github:deevus/pixels
```

Or via `go install`:

```bash
go install github.com/deevus/pixels@latest
```

Or build from source:

```bash
git clone https://github.com/deevus/pixels.git
cd pixels
go build
```

## Quick Start

```bash
# Create a base container with agent egress restrictions
pixels create base --egress agent --console

# Set up your environment, install dependencies, etc.
# Then save a checkpoint
pixels checkpoint create base --label ready

# Spin up new containers from the checkpoint
pixels create task1 --from base:ready
pixels create task2 --from base:ready

# Or clone from a container's current state
pixels create task3 --from base

# Tear down when done
pixels destroy task1
pixels destroy task2
```

## Commands

| Command | Description |
|---------|-------------|
| `pixels create <name>` | Create a new container |
| `pixels start <name>` | Start a stopped container |
| `pixels stop <name>` | Stop a running container |
| `pixels destroy <name>` | Permanently destroy a container and all its checkpoints |
| `pixels list` | List all containers with status and IP |
| `pixels console <name>` | Open an interactive session |
| `pixels exec <name> -- <command...>` | Run a command in the container |
| `pixels checkpoint create <name>` | Create a snapshot |
| `pixels checkpoint list <name>` | List checkpoints with sizes |
| `pixels checkpoint restore <name> <label>` | Restore to a checkpoint |
| `pixels checkpoint delete <name> <label>` | Delete a checkpoint |
| `pixels network show <name>` | Show current egress rules |
| `pixels network set <name> <mode>` | Set egress mode |
| `pixels network allow <name> <domain>` | Add a domain to the allowlist |
| `pixels network deny <name> <domain>` | Remove a domain from the allowlist |

Global flags: `-v/--verbose`

## Container Lifecycle

```bash
# Create with custom resources
pixels create mybox --image ubuntu/24.04 --cpu 4 --memory 4096

# Create with agent sandbox and open console when ready
pixels create mybox --egress agent --console

# Skip all provisioning (no SSH keys, devtools, or egress setup)
pixels create mybox --no-provision

# Clone from an existing container's checkpoint
pixels create newbox --from mybox:ready

# Clone from an existing container's current state
pixels create newbox --from mybox
```

All containers are prefixed `px-` internally. Commands accept bare names (e.g., `mybox` becomes `px-mybox`).

## Console and Exec

The **Incus backend** uses the native Incus exec API over WebSocket. No SSH needed. The **TrueNAS backend** uses SSH to reach the container.

**Console** opens an interactive session with zmx session persistence.
Disconnecting and reconnecting re-attaches to the same session:

```bash
pixels console mybox                    # default "console" session
pixels console mybox -s build           # named session
pixels console mybox --no-persist       # plain SSH, no zmx
```

Inside a session, press `Ctrl+\` to detach (works in TUIs too), or type `detach`.

**Sessions** lists zmx sessions in a container:

```bash
pixels sessions mybox
```

**Exec** runs a command and returns its exit code:

```bash
pixels exec mybox -- ls -la /home/pixel
```

With the TrueNAS backend, SSH key auth is verified on connect. If it fails, pixels writes your machine's public key into the container.

## Checkpoints

Checkpoints are snapshots of the container's root filesystem. The underlying mechanism depends on the storage backend (ZFS, btrfs, etc.).

```bash
# Create with auto-generated label
pixels checkpoint create mybox

# Create with a custom label
pixels checkpoint create mybox --label ready

# List checkpoints
pixels checkpoint list mybox
# LABEL       SIZE
# ready       42.0 MiB

# Restore (stops container, rolls back, restarts)
pixels checkpoint restore mybox ready

# Delete
pixels checkpoint delete mybox ready
```

Clone a new container from a checkpoint to get identical copies:

```bash
pixels create worker1 --from mybox:ready
pixels create worker2 --from mybox:ready
```

The `checkpoint` command can also be abbreviated as `cp`.

## Agent Provisioning

By default, new containers are provisioned with:

- SSH public key injection (root + `pixel` user)
- DNS configuration via systemd-resolved
- Environment variables from config written to `/etc/environment`
- **Dev tools**: mise, Node.js LTS, Claude Code, Codex, and OpenCode (installed via a background systemd service)

Dev tools install asynchronously after container creation via [zmx](https://zmx.sh) sessions. Use `--console` to wait for provisioning to finish before dropping into a shell, or check progress with:

```bash
pixels status mybox
```

Disable provisioning entirely with `--no-provision`, or just dev tools via config:

```toml
[provision]
devtools = false
```

## Network Egress

Control outbound network access with three modes:

| Mode | Description |
|------|-------------|
| `unrestricted` | No filtering (default) |
| `agent` | Preset allowlist: AI APIs, package registries, Git/GitHub, Ubuntu repos, plus any custom domains |
| `allowlist` | Custom domain list only |

### Setting Egress at Creation

```bash
pixels create mybox --egress agent
```

### Changing Egress on a Running Container

```bash
# Switch to agent mode
pixels network set mybox agent

# Switch to unrestricted
pixels network set mybox unrestricted

# Show current rules
pixels network show mybox
```

### Managing the Allowlist

```bash
# Add a domain
pixels network allow mybox api.example.com

# Remove a domain
pixels network deny mybox api.example.com
```

The `agent` preset includes domains for Anthropic, OpenAI, Google AI, npm, PyPI, crates.io, Go proxy, GitHub (including release CDN), mise, Node.js, and Ubuntu package repos. CIDR ranges are included for Google and GitHub/Azure CDN IPs.

Egress is enforced via nftables rules inside the container with restricted sudo access. See [SECURITY.md](SECURITY.md) for known limitations and mitigations.

## Configuration

Create `~/.config/pixels/config.toml`:

```toml
# backend = "incus"          # default; or "truenas"

[incus]
# socket = ""                # local unix socket (default: /var/lib/incus/unix.socket)
# remote = ""                # remote Incus HTTPS URL
# client_cert = ""           # TLS client cert for remote
# client_key = ""            # TLS client key for remote
# server_cert = ""           # server CA cert for remote
# project = ""               # Incus project name

[truenas]
# host = "truenas.local"
# api_key: prefer PIXELS_TRUENAS_API_KEY env var over storing here
# username = "root"           # default
# insecure_skip_verify = false # default; set true for self-signed certs

[defaults]
# image = "ubuntu/24.04"     # default
# cpu = "2"                  # default
# memory = 2048              # MiB, default
# pool = "tank"              # discovered from server; override if needed
# network = ""               # Incus network name (e.g. "incusbr0")
# dns = ["1.1.1.1"]          # optional; nameservers to inject into containers

[ssh]
# user = "pixel"             # default
# key = "~/.ssh/id_ed25519"  # default

[provision]
# enabled = true             # default
# devtools = true            # default

[network]
# egress = "unrestricted"    # default
# allow = ["api.example.com"]  # additional domains for agent/allowlist modes

[env]
# Image vars — written to /etc/environment inside the container:
# ANTHROPIC_API_KEY = "sk-ant-..."
# OPENAI_API_KEY = "sk-..."
#
# Forward from host env — passed to console/exec sessions at runtime:
# ANTHROPIC_API_KEY = { forward = true }
#
# Session-only literal — passed to console/exec but NOT written to /etc/environment:
# MY_VAR = { value = "some-value", session_only = true }

[mcp]
# prefix = "mcp-"               # name prefix for MCP-spawned sandboxes (final: px-mcp-<hex>)
# base_prefix = "base-"         # name prefix for bases (final: px-base-<name>)
# default_image = ""            # falls back to defaults.image when empty
# listen_addr = "127.0.0.1:8765"
# endpoint_path = "/mcp"
# idle_stop_after = "1h"        # stop sandboxes idle for this long
# hard_destroy_after = "24h"    # destroy sandboxes older than this
# reap_interval = "1m"          # how often the reaper checks lifetimes
# exec_timeout_max = "10m"      # ceiling for any single MCP exec call
# state_file = ""               # default: $XDG_CACHE_HOME/pixels/mcp-state.json
# pid_file = ""                 # default: $XDG_CACHE_HOME/pixels/mcp.pid

# Declare extra bases (dev/python/node ship built in):
# [mcp.bases.rust]
# parent_image = "ubuntu/24.04"
# setup_script = "~/.config/pixels/bases/rust.sh"
# description  = "Rust toolchain"
```

### Priority Order

1. TOML config file (`~/.config/pixels/config.toml`)
2. Environment variables (`PIXELS_BACKEND`, `PIXELS_TRUENAS_HOST`, etc.)

### Environment Variables

| Variable | Config key |
|----------|-----------|
| `PIXELS_BACKEND` | `backend` |
| `PIXELS_INCUS_SOCKET` | `incus.socket` |
| `PIXELS_INCUS_REMOTE` | `incus.remote` |
| `PIXELS_INCUS_CLIENT_CERT` | `incus.client_cert` |
| `PIXELS_INCUS_CLIENT_KEY` | `incus.client_key` |
| `PIXELS_INCUS_SERVER_CERT` | `incus.server_cert` |
| `PIXELS_INCUS_PROJECT` | `incus.project` |
| `PIXELS_TRUENAS_HOST` | `truenas.host` |
| `PIXELS_TRUENAS_USERNAME` | `truenas.username` |
| `PIXELS_TRUENAS_API_KEY` | `truenas.api_key` |
| `PIXELS_TRUENAS_PORT` | `truenas.port` |
| `PIXELS_TRUENAS_INSECURE` | `truenas.insecure_skip_verify` |
| `PIXELS_DEFAULT_IMAGE` | `defaults.image` |
| `PIXELS_DEFAULT_CPU` | `defaults.cpu` |
| `PIXELS_DEFAULT_MEMORY` | `defaults.memory` |
| `PIXELS_DEFAULT_POOL` | `defaults.pool` |
| `PIXELS_SSH_USER` | `ssh.user` |
| `PIXELS_SSH_KEY` | `ssh.key` |
| `PIXELS_PROVISION_ENABLED` | `provision.enabled` |
| `PIXELS_PROVISION_DEVTOOLS` | `provision.devtools` |
| `PIXELS_NETWORK_EGRESS` | `network.egress` |
| `PIXELS_MCP_PREFIX` | `mcp.prefix` |
| `PIXELS_MCP_BASE_PREFIX` | `mcp.base_prefix` |
| `PIXELS_MCP_DEFAULT_IMAGE` | `mcp.default_image` |
| `PIXELS_MCP_LISTEN_ADDR` | `mcp.listen_addr` |
| `PIXELS_MCP_ENDPOINT_PATH` | `mcp.endpoint_path` |
| `PIXELS_MCP_IDLE_STOP_AFTER` | `mcp.idle_stop_after` |
| `PIXELS_MCP_HARD_DESTROY_AFTER` | `mcp.hard_destroy_after` |
| `PIXELS_MCP_REAP_INTERVAL` | `mcp.reap_interval` |
| `PIXELS_MCP_EXEC_TIMEOUT_MAX` | `mcp.exec_timeout_max` |
| `PIXELS_MCP_STATE_FILE` | `mcp.state_file` |
| `PIXELS_MCP_PID_FILE` | `mcp.pid_file` |

## Using `pixels` as an MCP code-sandbox server

> **Alpha.** Lifecycle and the tool surface are stable enough to build
> against. Sandbox security for the MCP path is not yet aligned with
> `pixels create`: sandboxes don't get per-call egress policies, and
> base clones inherit whatever was baked in at build time. Egress
> and the rest of the `pixels create` hardening are coming soon.
> For now, I'd treat an MCP-spawned sandbox as if you'd run
> `pixels create --egress unrestricted`.

`pixels mcp` runs a streamable-HTTP MCP server that exposes container
lifecycle, exec, and file CRUD as MCP tools. Run it once on your
machine, then point any number of MCP clients at it.

### Start the daemon

    pixels mcp

By default it binds to `http://127.0.0.1:8765/mcp` and refuses to start
if another instance is already running (PID file at
`~/.cache/pixels/mcp.pid`).

The server has no auth. Keep it on loopback, or put it behind a
reverse proxy (Caddy, nginx, Traefik) that handles auth for you.
If you bind it to a non-loopback address with no proxy in front,
anything that can reach the port can run `exec` in any of your
sandboxes.

### Configure your client

Claude Code MCP entry:

    {
      "mcpServers": {
        "pixels": {
          "type": "http",
          "url": "http://127.0.0.1:8765/mcp"
        }
      }
    }

### Tools

| Tool | What it does |
|---|---|
| `create_sandbox` | Spin up a new ephemeral container (`base` for fast clone, `image` for raw) |
| `list_sandboxes` | List tracked sandboxes (with status, error, IP) |
| `list_bases` | List declared base pixels and their status |
| `start_sandbox` / `stop_sandbox` / `destroy_sandbox` | Lifecycle |
| `exec` | Run a command inside a sandbox |
| `write_file` | Create or fully overwrite a file |
| `read_file` | Read a file (optional truncation via `max_bytes`) |
| `edit_file` | Replace `old_string` with `new_string` (with optional `replace_all`) |
| `delete_file` | Remove a file |
| `list_files` | List directory contents (optionally recursive) |

### Container names

Both backends prepend `px-` to every instance. The MCP daemon
prepends its own prefix on top of that, so:

- MCP-spawned sandboxes land at `px-mcp-<hex>` (`mcp.prefix` default
  is `mcp-`).
- Bases land at `px-base-<name>` (`mcp.base_prefix` default is
  `base-`).

That's why CLI examples like `pixels checkpoint create px-base-python`
use the full on-disk name. `create_sandbox` returns the full name in
its response.

### Base pixels

A base is a container that sandboxes clone from. Bases are declared in
config (or shipped as defaults) and built on demand.

Three bases ship out of the box:

- `dev` — Ubuntu 24.04 + git, curl, wget, jq, vim, build-essential
- `python` — `dev` + python3, pip, pipx, venv
- `node` — `dev` + Node 22 LTS, npm

Add your own in config. Each base must declare exactly one of `parent_image` or `from`:

```toml
[mcp.bases.rust]
parent_image = "ubuntu/24.04"
setup_script = "~/.config/pixels/bases/rust.sh"
description  = "Rust toolchain"
```

Or build on top of another base:

```toml
[mcp.bases.rust-dev]
from = "dev"
setup_script = "~/.config/pixels/bases/rust-dev.sh"
description  = "Rust toolchain + dev tools"
```

Bases form a DAG via `from`. Cycle / missing-dep / both-set / neither-set
are rejected at config load.

**Setup scripts run as root with the `pixels create` hardening absent.**
I treat them like Docker `RUN` lines: only build a base from a script
you wrote or reviewed. The egress, `safe-apt`, and restricted-sudoers
wiring from `pixels create` isn't applied during base build.

Customise a base by mutating its container:

```bash
pixels start px-base-python
pixels exec px-base-python -- apt install vim
pixels stop px-base-python
pixels checkpoint create px-base-python
```

The next `create_sandbox(base="python")` call clones from the new
checkpoint. Existing sandboxes are unaffected (independent containers).

**Checkpoint-advances-clone-source.** Future sandboxes clone from the
most recent checkpoint by creation time, not by label. So any
`pixels checkpoint create px-base-X` immediately advances the clone
source. If you take a *safety* checkpoint before mutating, new sandboxes
clone from that pre-mutation state until you take another checkpoint
*after* the changes. Always re-checkpoint after mutating to ensure new
clones pick up your changes.

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

Example `pixels base list` output:

```
$ pixels base list
NAME    FROM/IMAGE              STATUS  LAST_CHECKPOINT       DESCRIPTION
dev     ubuntu/24.04            ready   2026-04-27 12:30:00   Ubuntu 24.04 + git, curl, vim, ...
node    dev                     missing                       dev + Node 22 LTS, npm
python  dev                     ready   2026-04-27 12:35:00   dev + python3, pip, pipx, venv
```

**Force rebuild.** There is no `pixels base rebuild` command. To force a full
rebuild of a base, run `pixels destroy px-base-<name> && pixels base build <name>`.

### Provisioning is async

`create_sandbox` returns immediately with `status: "provisioning"`.
The agent should poll `list_sandboxes` until status flips to `running`
or `failed`. A failed sandbox includes an `error` field describing
what went wrong.

For simple use without a base, provisioning takes ~30s. With a built
base, ~5s. With an unbuilt base, several minutes (the build runs
behind the scenes).

### Lifetimes

Two TTLs apply (configurable in `[mcp]` config):

- `idle_stop_after` (default 1h) — running sandbox with no recent
  activity gets stopped.
- `hard_destroy_after` (default 24h) — any sandbox older than this is
  destroyed and removed from state.

## Security

Container egress filtering uses nftables rules inside the container. A root process with `cap_net_admin` could bypass these rules. The `pixel` user has restricted sudo that only permits safe-apt, dpkg-query, systemctl, journalctl, and nft list.

See [SECURITY.md](SECURITY.md) for the full threat model, known issues, and mitigations.

## Support

Need help deploying pixels at your org? I'll get it running, build whatever you need on top, and walk your team through it. Find me at [simonhartcher.com](https://simonhartcher.com). Email in bio.

## License

[MIT](LICENSE)
