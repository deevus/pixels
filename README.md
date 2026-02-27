# pixels

Disposable Linux containers for AI coding agents, powered by TrueNAS and Incus.

Spin up sandboxed Linux containers pre-loaded with AI coding tools (Claude Code, Codex, OpenCode via mise). Each container gets SSH access, ZFS snapshot-based checkpoints, and network egress policies that control what the agent can reach. Managed entirely from the CLI over TrueNAS WebSocket API.

## Features

- **Container lifecycle** -- create, start, stop, destroy, and list Incus containers
- **SSH access** -- interactive console and remote command execution
- **ZFS checkpoints** -- snapshot, restore, delete, and clone containers from checkpoints
- **AI agent provisioning** -- automatically installs mise, Claude Code, Codex, and OpenCode
- **Network egress policies** -- restrict outbound traffic to AI APIs, package registries, and Git (or a custom allowlist)
- **Configuration** -- TOML config file, `PIXELS_*` environment variables, and CLI flags
- **Local caching** -- disk cache avoids WebSocket round-trips for console/exec

## Prerequisites

- [TrueNAS SCALE](https://www.truenas.com/truenas-scale/) with Incus virtualization enabled
- TrueNAS API key (create one in the TrueNAS web UI under Credentials > API Keys)
- Go 1.25+ (for building from source)
- SSH key pair (defaults to `~/.ssh/id_ed25519`)

## Installation

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
| `pixels console <name>` | Open an interactive SSH session |
| `pixels exec <name> -- <command...>` | Run a command via SSH |
| `pixels checkpoint create <name>` | Create a ZFS snapshot |
| `pixels checkpoint list <name>` | List checkpoints with sizes |
| `pixels checkpoint restore <name> <label>` | Restore to a checkpoint |
| `pixels checkpoint delete <name> <label>` | Delete a checkpoint |
| `pixels network show <name>` | Show current egress rules |
| `pixels network set <name> <mode>` | Set egress mode |
| `pixels network allow <name> <domain>` | Add a domain to the allowlist |
| `pixels network deny <name> <domain>` | Remove a domain from the allowlist |

Global flags: `--host`, `--api-key`, `-u/--username`, `-v/--verbose`

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

## SSH Access

**Console** opens an interactive SSH session. If the container is stopped, it starts it automatically:

```bash
pixels console mybox
```

**Exec** runs a command and returns its exit code:

```bash
pixels exec mybox -- ls -la /home/pixel
```

Both commands check the local cache first for the container's IP, falling back to the TrueNAS API. SSH key auth is verified on connect -- if it fails, the current machine's public key is automatically written to the container.

## Checkpoints

Checkpoints are ZFS snapshots of the container's root filesystem.

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

Dev tools install asynchronously after container creation. Use `--console` to wait for them to finish before dropping into a shell, or monitor progress with:

```bash
pixels exec mybox -- sudo journalctl -fu pixels-devtools
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
[truenas]
host = "truenas.local"
# api_key: prefer PIXELS_TRUENAS_API_KEY env var over storing here
# username = "root"           # default
# insecure_skip_verify = false # default; set true for self-signed certs

[defaults]
# image = "ubuntu/24.04"     # default
# cpu = "2"                  # default
# memory = 2048              # MiB, default
# pool = "tank"              # discovered from server; override if needed
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
# ANTHROPIC_API_KEY = "sk-ant-..."
# OPENAI_API_KEY = "sk-..."
```

### Priority Order

1. TOML config file (`~/.config/pixels/config.toml`)
2. Environment variables (`PIXELS_TRUENAS_HOST`, `PIXELS_TRUENAS_API_KEY`, etc.)
3. CLI flags (`--host`, `--api-key`, `-u`)

### Environment Variables

| Variable | Config key |
|----------|-----------|
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
| `PIXELS_CHECKPOINT_DATASET_PREFIX` | `checkpoint.dataset_prefix` |
| `PIXELS_PROVISION_ENABLED` | `provision.enabled` |
| `PIXELS_PROVISION_DEVTOOLS` | `provision.devtools` |
| `PIXELS_NETWORK_EGRESS` | `network.egress` |

## Security

Container egress filtering uses nftables rules inside the container. A root process with `cap_net_admin` could bypass these rules. The `pixel` user has restricted sudo that only permits safe-apt, dpkg-query, systemctl, journalctl, and nft list.

See [SECURITY.md](SECURITY.md) for the full threat model, known issues, and mitigations.

## License

[MIT](LICENSE)
