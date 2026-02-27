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
