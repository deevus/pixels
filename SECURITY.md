# Security

## Egress Firewall

The egress firewall uses nftables rules **inside** the container. This means a root user inside the container can flush the rules. The `safe-apt` wrapper prevents the most common escalation path (apt-get Pre-Invoke hooks), but root access through other means would bypass the firewall entirely.

### Known Issues

#### Container-side firewall is bypassable with root

Any process running as root with `cap_net_admin` can run `nft flush ruleset` to remove all egress restrictions. Mitigations:

- **Drop `cap_net_admin`**: `incus config set <name> raw.lxc="lxc.cap.drop = net_admin"`
- **Move firewall to host side**: Apply nftables rules on the host filtering traffic from the container's IP, so the container cannot modify them.

#### Incus agent socket accessible

`/dev/incus/sock` is world-readable and exposes instance config, device layout, and pool names via the devIncus API.

- **Fix**: `incus config set <name> security.devlxd=false`

#### /dev/zfs accessible

The ZFS control device (`/dev/zfs`, mode 666) is accessible inside the container. No ZFS tools are installed by default, but they could be installed via the safe-apt wrapper.

- **Fix**: Remove the device from the container or restrict permissions via Incus device overrides.

#### No AppArmor profile

Containers run unconfined by AppArmor. The user namespace is the primary isolation boundary and prevents most kernel-level escapes.

- **Fix**: Use the default Incus AppArmor profile or create a custom one.

### Mitigating Factors

- **User namespaces are active**: Root inside the container maps to an unprivileged UID on the host (2147000001), preventing kernel-level escapes via debugfs, tracefs, sysrq, dmesg, and modprobe.
- **sudo is restricted**: The `safe-apt` wrapper blocks `-o` flags and only allows safe apt-get subcommands. Direct `apt-get`, `apt`, and `dpkg` are not in the NOPASSWD sudoers.

## MCP Server (`pixels mcp`)

`pixels mcp` is alpha. The MCP path has a different security posture from `pixels create`. Two known gaps.

### No authentication on the HTTP endpoint

The streamable-HTTP server has no auth and no transport security. The default bind is `127.0.0.1:8765`, so the loopback interface is the boundary. Any local process that can reach the port can call `exec` against any sandbox the daemon knows about. That includes another user on the box, a browser tab on a malicious page, or a dev container with host networking.

- **Mitigation**: Keep it on loopback, or front it with a reverse proxy (Caddy, nginx, Traefik) that handles auth and TLS.
- **Future work**: A shared-secret header or unix-socket transport as the default.

### Egress not aligned with `pixels create`

MCP-spawned sandboxes don't get per-call egress policies. Two paths, both with gaps:

- `provisionFromImage` runs the full provision flow, but pinned to whatever `network.egress` was set to when the daemon started. The MCP `create_sandbox` tool has no `egress` field.
- `provisionFromBase` clones from a base snapshot and applies nothing on top. The clone inherits whatever egress posture (or none) was baked into the base at build time.

Base setup scripts also run as root. The full `pixels create` hardening is absent: no `safe-apt`, no restricted sudoers, no nftables wiring.

- **Mitigation**: Until per-call egress lands, treat MCP-spawned sandboxes as `pixels create --egress unrestricted`. If you didn't write the base setup script yourself, don't build it on a sensitive network.
- **Future work**: An `egress` field on `create_sandbox`, re-pushing egress files after `CloneFrom`, and either running base setup under the restricted-sudoers regime or surfacing the trust requirement as a config-load warning.
