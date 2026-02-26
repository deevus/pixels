# Network Egress Policy Design

## Overview

Add configurable network egress policies to pixels containers, allowing users to restrict outbound network access. Primary use case: letting AI coding agents run in containers without unrestricted internet access.

## Configuration

New `[network]` section in `~/.config/pixels/config.toml`:

```toml
[network]
# "unrestricted" (default) | "agent" (preset) | "allowlist" (custom)
egress = "unrestricted"

# Custom domains (used when egress = "allowlist"; merged into "agent" preset)
allow = [
  "internal.mycompany.com",
  "registry.example.com",
]
```

### Egress Modes

- **`unrestricted`** — No firewall rules. Current behavior. Default.
- **`agent`** — Built-in preset for AI/dev workflows, plus any `allow` entries.
- **`allowlist`** — Only `allow` entries permitted, nothing else.

### Agent Preset Domains

- AI APIs: `api.anthropic.com`, `api.openai.com`, `generativelanguage.googleapis.com`
- Package registries: `registry.npmjs.org`, `pypi.org`, `files.pythonhosted.org`, `crates.io`, `static.crates.io`, `proxy.golang.org`
- Git: `github.com`, `gitlab.com`, `*.githubusercontent.com`
- Tools: `mise.run`, `mise.jdx.dev`, `nodejs.org`
- DNS: UDP 53 to configured nameservers (or 1.1.1.1/8.8.8.8 fallback)

## Implementation: nftables Inside the Container

### Why nftables (not Incus network ACLs)

TrueNAS 25.04 API has no methods for Incus network ACLs, firewall rules, or egress policies. The `virt.instance.device_add` PROXY device is port-forwarding only. nftables is self-contained within the container, ships with Ubuntu 24.04, and doesn't depend on TrueNAS API surface.

### Provisioned Files (rootfs writes before boot)

| File | Purpose |
|------|---------|
| `/etc/pixels-egress-domains` | One domain per line |
| `/etc/nftables.conf` | Base nftables ruleset |
| `/usr/local/bin/pixels-resolve-egress.sh` | Resolves domains to IPs, loads nft rules |
| `/etc/sudoers.d/pixel` | Allowlist sudo (replaces blanket NOPASSWD:ALL) |

### nftables Ruleset

```
table inet pixels_egress {
    set allowed_v4 {
        type ipv4_addr
        flags interval
    }

    chain output {
        type filter hook output priority 0; policy drop;

        oif lo accept
        ct state established,related accept
        udp dport 53 accept
        udp dport 67-68 accept
        tcp sport 22 accept

        ip daddr @allowed_v4 accept

        log prefix "pixels-egress-denied: " drop
    }
}
```

### Boot Sequence (rc.local additions)

1. `apt-get install -y -qq nftables dnsutils`
2. Run `pixels-resolve-egress.sh` (resolve domains, load rules)
3. `systemctl enable nftables`

## Agent Tamper Protection

When egress is `agent` or `allowlist`, the pixel user gets allowlist-only sudo instead of blanket access:

```
pixel ALL=(ALL) NOPASSWD: /usr/bin/apt-get, /usr/bin/apt, \
    /usr/bin/dpkg, \
    /usr/bin/systemctl start *, /usr/bin/systemctl stop *, \
    /usr/bin/systemctl restart *, /usr/bin/systemctl status *, \
    /usr/bin/systemctl enable *, /usr/bin/systemctl disable *, \
    /usr/bin/journalctl
```

No access to `nft`, `iptables`, `chattr`, or arbitrary root shell. Egress files owned `root:root 0644`.

In `unrestricted` mode, keep existing blanket `pixel ALL=(ALL) NOPASSWD:ALL`.

## CLI Commands

### New `pixels network` command

- `pixels network show <name>` — display current rules and allowed domains
- `pixels network set <name> <mode>` — change egress mode on running container
- `pixels network allow <name> <domain>` — add domain to allowlist
- `pixels network deny <name> <domain>` — remove domain from allowlist

Runtime updates SSH as `root@<ip>` to bypass pixel user's sudo restrictions, rewrite domain list, and reload nftables.

### New `create` flag

- `--egress <mode>` — override config default (e.g. `pixels create mybox --egress agent`)

## Data Flow

```
pixels create mybox --egress agent
│
├─ Provision (rootfs writes before boot):
│   ├─ /etc/pixels-egress-domains
│   ├─ /etc/nftables.conf
│   ├─ /usr/local/bin/pixels-resolve-egress.sh
│   ├─ /etc/sudoers.d/pixel              (allowlist sudo)
│   └─ /etc/rc.local                     (calls resolve script)
│
├─ Boot (rc.local):
│   ├─ install openssh-server, create pixel user (existing)
│   └─ install nftables, run resolve script
│
└─ Container running with egress filtered

pixels network allow mybox custom.api.com
│
├─ SSH as root@<ip>
├─ Append to /etc/pixels-egress-domains
└─ Run pixels-resolve-egress.sh (reload)
```

## What Doesn't Change

- `unrestricted` mode: zero new files, current behavior exactly
- All existing commands: `console`, `exec`, `start`, `stop`, `destroy`, `checkpoint`
- Cache behavior

## Known Limitations

- DNS resolution is point-in-time. If IPs change, rules go stale until reboot or manual `pixels network set`. Matches sprites' behavior.
- Sudoers allowlist blocks standard paths but not raw syscalls (e.g. a compiled C program). Sufficient for AI agent threat model.
- Wildcard domains (e.g. `*.githubusercontent.com`) require resolving the specific subdomains used.

## Future Enhancements (not v1)

- Periodic DNS re-resolution via cron inside the container
- CIDR entries in the allowlist
- Port-level granularity
