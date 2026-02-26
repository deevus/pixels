# Network Egress Policy Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add configurable nftables-based egress controls to pixels containers so AI agents can run with restricted outbound network access.

**Architecture:** New `[network]` config section with three modes (unrestricted/agent/allowlist). During provisioning, write nftables rules + domain resolve script + restricted sudoers into the container rootfs. New `pixels network` command for runtime updates via SSH as root.

**Tech Stack:** Go (cobra commands, TOML config), nftables (in-container), shell scripts (domain resolution)

**Design doc:** `docs/plans/2026-02-26-network-egress-design.md`

---

### Task 1: Add Network config to config package

**Files:**
- Modify: `internal/config/config.go:13-19` (Config struct)
- Modify: `internal/config/config.go:69-114` (Load function)
- Test: `internal/config/config_test.go`

**Step 1: Write the failing test**

Add to `internal/config/config_test.go`:

```go
func TestNetworkDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, key := range []string{
		"PIXELS_TRUENAS_HOST", "PIXELS_TRUENAS_USERNAME", "PIXELS_TRUENAS_API_KEY",
		"PIXELS_NETWORK_EGRESS",
	} {
		t.Setenv(key, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Network.Egress != "unrestricted" {
		t.Errorf("Network.Egress = %q, want %q", cfg.Network.Egress, "unrestricted")
	}
	if cfg.Network.Allow != nil {
		t.Errorf("Network.Allow = %v, want nil", cfg.Network.Allow)
	}
}

func TestNetworkFromFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfgDir := filepath.Join(dir, "pixels")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `
[network]
egress = "agent"
allow = ["internal.mycompany.com", "registry.example.com"]
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PIXELS_NETWORK_EGRESS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Network.Egress != "agent" {
		t.Errorf("Network.Egress = %q, want %q", cfg.Network.Egress, "agent")
	}
	if len(cfg.Network.Allow) != 2 {
		t.Fatalf("Network.Allow len = %d, want 2", len(cfg.Network.Allow))
	}
	if cfg.Network.Allow[0] != "internal.mycompany.com" {
		t.Errorf("Network.Allow[0] = %q, want %q", cfg.Network.Allow[0], "internal.mycompany.com")
	}
}

func TestNetworkEnvOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PIXELS_NETWORK_EGRESS", "allowlist")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Network.Egress != "allowlist" {
		t.Errorf("Network.Egress = %q, want %q", cfg.Network.Egress, "allowlist")
	}
}

func TestNetworkIsRestricted(t *testing.T) {
	tests := []struct {
		egress string
		want   bool
	}{
		{"unrestricted", false},
		{"agent", true},
		{"allowlist", true},
	}
	for _, tt := range tests {
		t.Run(tt.egress, func(t *testing.T) {
			n := Network{Egress: tt.egress}
			if got := n.IsRestricted(); got != tt.want {
				t.Errorf("IsRestricted() = %v, want %v", got, tt.want)
			}
		})
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestNetwork -v`
Expected: FAIL — `Network` type and field don't exist yet.

**Step 3: Write minimal implementation**

In `internal/config/config.go`, add the `Network` struct and field:

```go
type Network struct {
	Egress string   `toml:"egress"`
	Allow  []string `toml:"allow"`
}

func (n *Network) IsRestricted() bool {
	return n.Egress == "agent" || n.Egress == "allowlist"
}
```

Add `Network Network \`toml:"network"\`` to the `Config` struct.

In `Load()`, set the default `Network: Network{Egress: "unrestricted"}` and add the env override:

```go
applyEnv(&cfg.Network.Egress, "PIXELS_NETWORK_EGRESS")
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestNetwork -v`
Expected: PASS

**Step 5: Run all existing tests to verify no regressions**

Run: `go test ./...`
Expected: All PASS

**Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: add [network] config section with egress mode and allow list"
```

---

### Task 2: Add egress package with agent preset and domain resolution

**Files:**
- Create: `internal/egress/egress.go`
- Create: `internal/egress/egress_test.go`

**Step 1: Write the failing test**

Create `internal/egress/egress_test.go`:

```go
package egress

import (
	"strings"
	"testing"
)

func TestAgentDomains(t *testing.T) {
	domains := AgentDomains()
	if len(domains) == 0 {
		t.Fatal("AgentDomains() returned empty")
	}

	// Spot-check critical domains.
	required := []string{
		"api.anthropic.com",
		"api.openai.com",
		"registry.npmjs.org",
		"github.com",
		"pypi.org",
	}
	domainSet := make(map[string]bool)
	for _, d := range domains {
		domainSet[d] = true
	}
	for _, r := range required {
		if !domainSet[r] {
			t.Errorf("AgentDomains() missing %q", r)
		}
	}
}

func TestResolvedDomains(t *testing.T) {
	tests := []struct {
		name   string
		egress string
		allow  []string
		want   int // minimum expected domains
	}{
		{"unrestricted returns nil", "unrestricted", nil, 0},
		{"agent returns preset", "agent", nil, 10},
		{"agent merges allow", "agent", []string{"custom.example.com"}, 11},
		{"allowlist returns only allow", "allowlist", []string{"a.com", "b.com"}, 2},
		{"allowlist empty returns empty", "allowlist", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveDomains(tt.egress, tt.allow)
			if tt.egress == "unrestricted" {
				if got != nil {
					t.Errorf("got %v, want nil for unrestricted", got)
				}
				return
			}
			if len(got) < tt.want {
				t.Errorf("got %d domains, want >= %d", len(got), tt.want)
			}
			// Check no duplicates.
			seen := make(map[string]bool)
			for _, d := range got {
				if seen[d] {
					t.Errorf("duplicate domain: %s", d)
				}
				seen[d] = true
			}
		})
	}
}

func TestNftablesConf(t *testing.T) {
	conf := NftablesConf()
	if !strings.Contains(conf, "table inet pixels_egress") {
		t.Error("missing table definition")
	}
	if !strings.Contains(conf, "policy drop") {
		t.Error("missing drop policy")
	}
	if !strings.Contains(conf, "@allowed_v4") {
		t.Error("missing allowed_v4 set reference")
	}
	if !strings.Contains(conf, "oif lo accept") {
		t.Error("missing loopback rule")
	}
}

func TestResolveScript(t *testing.T) {
	script := ResolveScript()
	if !strings.Contains(script, "#!/bin/bash") {
		t.Error("missing shebang")
	}
	if !strings.Contains(script, "pixels-egress-domains") {
		t.Error("missing domain file reference")
	}
	if !strings.Contains(script, "nft") {
		t.Error("missing nft command")
	}
}

func TestDomainsFileContent(t *testing.T) {
	domains := []string{"api.anthropic.com", "github.com"}
	content := DomainsFileContent(domains)
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) != 2 {
		t.Errorf("got %d lines, want 2", len(lines))
	}
	if lines[0] != "api.anthropic.com" {
		t.Errorf("line[0] = %q, want %q", lines[0], "api.anthropic.com")
	}
}

func TestSudoersRestricted(t *testing.T) {
	s := SudoersRestricted()
	if !strings.Contains(s, "/usr/bin/apt-get") {
		t.Error("missing apt-get allowlist")
	}
	if !strings.Contains(s, "/usr/bin/journalctl") {
		t.Error("missing journalctl allowlist")
	}
	if strings.Contains(s, "ALL=(ALL) NOPASSWD: ALL") {
		t.Error("should not contain blanket NOPASSWD:ALL")
	}
}

func TestSudoersUnrestricted(t *testing.T) {
	s := SudoersUnrestricted()
	if !strings.Contains(s, "NOPASSWD: ALL") {
		t.Error("missing blanket NOPASSWD:ALL")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/egress/ -v`
Expected: FAIL — package doesn't exist.

**Step 3: Write minimal implementation**

Create `internal/egress/egress.go`:

```go
package egress

import "strings"

// AgentDomains returns the built-in domain allowlist for the "agent" preset.
func AgentDomains() []string {
	return []string{
		// AI APIs
		"api.anthropic.com",
		"api.openai.com",
		"generativelanguage.googleapis.com",
		// Package registries
		"registry.npmjs.org",
		"pypi.org",
		"files.pythonhosted.org",
		"crates.io",
		"static.crates.io",
		"proxy.golang.org",
		"sum.golang.org",
		// Git
		"github.com",
		"gitlab.com",
		"objects.githubusercontent.com",
		"raw.githubusercontent.com",
		"codeload.github.com",
		// Tools
		"mise.run",
		"mise.jdx.dev",
		"nodejs.org",
	}
}

// ResolveDomains returns the final domain list for the given egress mode.
// Returns nil for "unrestricted".
func ResolveDomains(egress string, allow []string) []string {
	switch egress {
	case "unrestricted":
		return nil
	case "agent":
		seen := make(map[string]bool)
		var merged []string
		for _, d := range AgentDomains() {
			if !seen[d] {
				seen[d] = true
				merged = append(merged, d)
			}
		}
		for _, d := range allow {
			if !seen[d] {
				seen[d] = true
				merged = append(merged, d)
			}
		}
		return merged
	case "allowlist":
		return allow
	default:
		return nil
	}
}

// DomainsFileContent returns the content of /etc/pixels-egress-domains.
func DomainsFileContent(domains []string) string {
	return strings.Join(domains, "\n") + "\n"
}

// NftablesConf returns the base nftables.conf content.
func NftablesConf() string {
	return `#!/usr/sbin/nft -f
flush ruleset

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
`
}

// ResolveScript returns the shell script that reads /etc/pixels-egress-domains,
// resolves each domain to IPs, and populates the nftables allowed_v4 set.
func ResolveScript() string {
	return `#!/bin/bash
set -euo pipefail

DOMAIN_FILE="/etc/pixels-egress-domains"
NFT_CONF="/etc/nftables.conf"

if [ ! -f "$DOMAIN_FILE" ]; then
    echo "No domain file found, skipping egress setup"
    exit 0
fi

# Load the base ruleset (creates table and empty set).
nft -f "$NFT_CONF"

# Resolve each domain and add IPs to the allowed set.
while IFS= read -r domain || [ -n "$domain" ]; do
    domain=$(echo "$domain" | xargs)
    [ -z "$domain" ] && continue
    [[ "$domain" == \#* ]] && continue

    ips=$(getent ahostsv4 "$domain" 2>/dev/null | awk '{print $1}' | sort -u || true)
    for ip in $ips; do
        nft add element inet pixels_egress allowed_v4 "{ $ip }" 2>/dev/null || true
    done
done < "$DOMAIN_FILE"

echo "Egress rules loaded: $(nft list set inet pixels_egress allowed_v4 | grep -c 'elements' || echo 0) entries"
`
}

// SudoersRestricted returns the sudoers content for restricted egress mode.
func SudoersRestricted() string {
	return `pixel ALL=(ALL) NOPASSWD: /usr/bin/apt-get, /usr/bin/apt, \
    /usr/bin/dpkg, /usr/bin/dpkg-query, \
    /usr/bin/systemctl start *, /usr/bin/systemctl stop *, \
    /usr/bin/systemctl restart *, /usr/bin/systemctl status *, \
    /usr/bin/systemctl enable *, /usr/bin/systemctl disable *, \
    /usr/bin/journalctl, /usr/bin/journalctl *
`
}

// SudoersUnrestricted returns the blanket sudoers content (current behavior).
func SudoersUnrestricted() string {
	return "pixel ALL=(ALL) NOPASSWD: ALL\n"
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/egress/ -v`
Expected: All PASS

**Step 5: Commit**

```bash
git add internal/egress/
git commit -m "feat: add egress package with agent preset, nftables conf, and resolve script"
```

---

### Task 3: Wire egress into provisioning

**Files:**
- Modify: `internal/truenas/client.go:72-77` (ProvisionOpts struct)
- Modify: `internal/truenas/client.go:82-181` (Provision function)
- Modify: `internal/truenas/client.go:183-238` (rc.local constants)
- Test: `internal/truenas/client_test.go`

**Step 1: Write the failing test**

Add to `internal/truenas/client_test.go`, inside the `TestProvision` table:

```go
{
    name: "egress agent provisioning",
    opts: ProvisionOpts{
        SSHPubKey: "ssh-ed25519 AAAA test@host",
        Egress:    "agent",
    },
    pool:      "tank",
    wantCalls: 7, // root key + pixel key + domains + nftables.conf + resolve script + sudoers + rc.local
    check: func(t *testing.T, calls []writeCall) {
        paths := make(map[string]writeCall)
        for _, c := range calls {
            paths[c.path] = c
        }
        rootfs := "/var/lib/incus/storage-pools/tank/containers/px-test/rootfs"

        // Egress domains file.
        domains := paths[rootfs+"/etc/pixels-egress-domains"]
        if !strings.Contains(domains.content, "api.anthropic.com") {
            t.Error("domains file missing api.anthropic.com")
        }

        // nftables.conf.
        nft := paths[rootfs+"/etc/nftables.conf"]
        if !strings.Contains(nft.content, "pixels_egress") {
            t.Error("nftables.conf missing pixels_egress table")
        }

        // Resolve script.
        script := paths[rootfs+"/usr/local/bin/pixels-resolve-egress.sh"]
        if script.mode != 0o755 {
            t.Errorf("resolve script mode = %o, want 755", script.mode)
        }

        // Sudoers should be restricted.
        sudoers := paths[rootfs+"/etc/sudoers.d/pixel"]
        if strings.Contains(sudoers.content, "NOPASSWD: ALL") {
            t.Error("sudoers should be restricted, not blanket ALL")
        }
        if !strings.Contains(sudoers.content, "/usr/bin/apt-get") {
            t.Error("sudoers missing apt-get allowlist")
        }

        // rc.local should include nftables setup.
        rc := paths[rootfs+"/etc/rc.local"]
        if !strings.Contains(rc.content, "nftables") {
            t.Error("rc.local missing nftables install")
        }
        if !strings.Contains(rc.content, "pixels-resolve-egress") {
            t.Error("rc.local missing resolve script call")
        }
    },
},
{
    name: "egress unrestricted skips egress files",
    opts: ProvisionOpts{
        SSHPubKey: "ssh-ed25519 AAAA test@host",
        Egress:    "unrestricted",
    },
    pool:      "tank",
    wantCalls: 3, // root key + pixel key + rc.local (no egress files)
    check: func(t *testing.T, calls []writeCall) {
        for _, c := range calls {
            if strings.Contains(c.path, "pixels-egress") || strings.Contains(c.path, "nftables") {
                t.Errorf("unexpected egress file in unrestricted mode: %s", c.path)
            }
        }
    },
},
{
    name: "egress allowlist with custom domains",
    opts: ProvisionOpts{
        SSHPubKey:    "ssh-ed25519 AAAA test@host",
        Egress:       "allowlist",
        EgressAllow:  []string{"custom.example.com"},
    },
    pool:      "tank",
    wantCalls: 7, // root key + pixel key + domains + nftables.conf + resolve script + sudoers + rc.local
    check: func(t *testing.T, calls []writeCall) {
        rootfs := "/var/lib/incus/storage-pools/tank/containers/px-test/rootfs"
        for _, c := range calls {
            if c.path == rootfs+"/etc/pixels-egress-domains" {
                if !strings.Contains(c.content, "custom.example.com") {
                    t.Error("domains file missing custom domain")
                }
                if strings.Contains(c.content, "api.anthropic.com") {
                    t.Error("allowlist mode should not include agent preset domains")
                }
                return
            }
        }
        t.Error("domains file not written")
    },
},
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/truenas/ -run TestProvision -v`
Expected: FAIL — `Egress` and `EgressAllow` fields don't exist on `ProvisionOpts`.

**Step 3: Write minimal implementation**

Add fields to `ProvisionOpts`:

```go
type ProvisionOpts struct {
	SSHPubKey   string
	DNS         []string
	Env         map[string]string
	DevTools    bool
	Egress      string   // "unrestricted", "agent", or "allowlist"
	EgressAllow []string // custom domains (merged into agent, standalone for allowlist)
}
```

Add an import for `"github.com/deevus/pixels/internal/egress"` to `client.go`.

In the `Provision` function, after the existing env/SSH/devtools writes and before the rc.local write, add egress file writes:

```go
// Write egress control files when egress mode is restricted.
isRestricted := opts.Egress == "agent" || opts.Egress == "allowlist"
if isRestricted {
    domains := egress.ResolveDomains(opts.Egress, opts.EgressAllow)
    if err := c.Filesystem.WriteFile(ctx, rootfs+"/etc/pixels-egress-domains", truenas.WriteFileParams{
        Content: []byte(egress.DomainsFileContent(domains)),
        Mode:    0o644,
    }); err != nil {
        return fmt.Errorf("writing egress domains: %w", err)
    }
    if err := c.Filesystem.WriteFile(ctx, rootfs+"/etc/nftables.conf", truenas.WriteFileParams{
        Content: []byte(egress.NftablesConf()),
        Mode:    0o644,
    }); err != nil {
        return fmt.Errorf("writing nftables.conf: %w", err)
    }
    if err := c.Filesystem.WriteFile(ctx, rootfs+"/usr/local/bin/pixels-resolve-egress.sh", truenas.WriteFileParams{
        Content: []byte(egress.ResolveScript()),
        Mode:    0o755,
    }); err != nil {
        return fmt.Errorf("writing egress resolve script: %w", err)
    }
    if err := c.Filesystem.WriteFile(ctx, rootfs+"/etc/sudoers.d/pixel", truenas.WriteFileParams{
        Content: []byte(egress.SudoersRestricted()),
        Mode:    0o440,
    }); err != nil {
        return fmt.Errorf("writing restricted sudoers: %w", err)
    }
}
```

Add new rc.local variants that include nftables setup. The existing `rcLocalSSH` and `rcLocalSSHDevtools` stay unchanged. Add two new ones:

```go
const rcLocalSSHEgress = `#!/bin/sh
set -e
if [ ! -f /root/.ssh-provisioned ]; then
    apt-get update -qq
    apt-get install -y -qq openssh-server sudo nftables dnsutils

    if ! id pixel >/dev/null 2>&1; then
        userdel -r ubuntu 2>/dev/null || true
        groupdel ubuntu 2>/dev/null || true
        groupadd -g 1000 pixel
        useradd -m -u 1000 -g 1000 -s /bin/bash -G sudo pixel
    fi
    cp -rn /etc/skel/. /home/pixel/
    mkdir -p /home/pixel/.ssh

    chown -R pixel:pixel /home/pixel
    chmod 700 /home/pixel/.ssh

    systemctl enable --now ssh
    /usr/local/bin/pixels-resolve-egress.sh
    touch /root/.ssh-provisioned
fi
exit 0
`

const rcLocalSSHDevtoolsEgress = `#!/bin/sh
set -e
if [ ! -f /root/.ssh-provisioned ]; then
    apt-get update -qq
    apt-get install -y -qq openssh-server sudo nftables dnsutils

    if ! id pixel >/dev/null 2>&1; then
        userdel -r ubuntu 2>/dev/null || true
        groupdel ubuntu 2>/dev/null || true
        groupadd -g 1000 pixel
        useradd -m -u 1000 -g 1000 -s /bin/bash -G sudo pixel
    fi
    cp -rn /etc/skel/. /home/pixel/
    mkdir -p /home/pixel/.ssh

    chown -R pixel:pixel /home/pixel
    chmod 700 /home/pixel/.ssh

    systemctl enable --now ssh
    /usr/local/bin/pixels-resolve-egress.sh
    touch /root/.ssh-provisioned
fi
if [ -f /etc/systemd/system/pixels-devtools.service ] && [ ! -f /root/.devtools-provisioned ]; then
    systemctl daemon-reload
    systemctl start pixels-devtools.service
fi
exit 0
`
```

Update the rc.local selection logic in `Provision` to choose the egress variants:

```go
if opts.SSHPubKey != "" {
    rcLocal := rcLocalSSH
    if isRestricted && opts.DevTools {
        rcLocal = rcLocalSSHDevtoolsEgress
    } else if isRestricted {
        rcLocal = rcLocalSSHEgress
    } else if opts.DevTools {
        rcLocal = rcLocalSSHDevtools
    }
    // ... existing WriteFile for rc.local
}
```

Note: the egress rc.local variants omit the `echo 'pixel ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/pixel` line because the sudoers file is written during provisioning instead.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/truenas/ -run TestProvision -v`
Expected: All PASS

**Step 5: Update existing Provision tests for new wantCalls counts**

The existing `"ssh key only"` test has `wantCalls: 3` but the `Egress` field will default to `""` (empty string), which is treated as unrestricted. No changes needed to existing tests — they pass an empty `Egress` field which maps to no egress files.

Verify: the existing `"full provisioning"` test doesn't set `Egress`, so no egress files should be written. The `wantCalls: 7` should still hold (those 7 are: dns + env + root key + pixel key + setup script + systemd unit + rc.local).

**Step 6: Run full test suite**

Run: `go test ./...`
Expected: All PASS

**Step 7: Commit**

```bash
git add internal/truenas/client.go internal/truenas/client_test.go
git commit -m "feat: wire egress provisioning into container rootfs setup"
```

---

### Task 4: Add --egress flag to create command

**Files:**
- Modify: `cmd/create.go:23-29` (flags)
- Modify: `cmd/create.go:170-207` (provisioning block)

**Step 1: Add the flag and wire it into provisioning**

In `cmd/create.go`, add the flag in `init()`:

```go
cmd.Flags().String("egress", "", "egress policy: unrestricted, agent, allowlist (default from config)")
```

In `runCreate`, read the flag and pass it to provisioning:

```go
egressMode, _ := cmd.Flags().GetString("egress")
if egressMode == "" {
    egressMode = cfg.Network.Egress
}
```

Then in the provisioning block, add to `provOpts`:

```go
provOpts := tnc.ProvisionOpts{
    SSHPubKey:   pubKey,
    DNS:         cfg.Defaults.DNS,
    Env:         cfg.Env,
    DevTools:    cfg.Provision.DevToolsEnabled(),
    Egress:      egressMode,
    EgressAllow: cfg.Network.Allow,
}
```

**Step 2: Build to verify compilation**

Run: `go build ./...`
Expected: Success

**Step 3: Commit**

```bash
git add cmd/create.go
git commit -m "feat: add --egress flag to create command"
```

---

### Task 5: Add pixels network command with show/set/allow/deny subcommands

**Files:**
- Create: `cmd/network.go`

This task uses SSH as `root@<ip>` to manage egress rules on running containers. It reuses the existing cache + API fallback pattern from console/exec.

**Step 1: Create `cmd/network.go`**

```go
package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/cache"
	"github.com/deevus/pixels/internal/egress"
	"github.com/deevus/pixels/internal/ssh"
)

func init() {
	networkCmd := &cobra.Command{
		Use:   "network",
		Short: "Manage container network egress policies",
	}

	networkCmd.AddCommand(&cobra.Command{
		Use:   "show <name>",
		Short: "Show current egress rules and allowed domains",
		Args:  cobra.ExactArgs(1),
		RunE:  runNetworkShow,
	})

	networkCmd.AddCommand(&cobra.Command{
		Use:   "set <name> <mode>",
		Short: "Set egress mode (unrestricted, agent, allowlist)",
		Args:  cobra.ExactArgs(2),
		RunE:  runNetworkSet,
	})

	networkCmd.AddCommand(&cobra.Command{
		Use:   "allow <name> <domain>",
		Short: "Add a domain to the container's egress allowlist",
		Args:  cobra.ExactArgs(2),
		RunE:  runNetworkAllow,
	})

	networkCmd.AddCommand(&cobra.Command{
		Use:   "deny <name> <domain>",
		Short: "Remove a domain from the container's egress allowlist",
		Args:  cobra.ExactArgs(2),
		RunE:  runNetworkDeny,
	})

	rootCmd.AddCommand(networkCmd)
}

// resolveContainerIP returns the IP for a container, checking cache first.
func resolveContainerIP(cmd *cobra.Command, name string) (string, error) {
	if entry := cache.Get(name); entry != nil && entry.Status == "RUNNING" && entry.IP != "" {
		return entry.IP, nil
	}

	ctx := cmd.Context()
	client, err := connectClient(ctx)
	if err != nil {
		return "", err
	}
	defer client.Close()

	instance, err := client.Virt.GetInstance(ctx, containerName(name))
	if err != nil {
		return "", fmt.Errorf("looking up %s: %w", name, err)
	}
	if instance.Status != "RUNNING" {
		return "", fmt.Errorf("%s is not running (status: %s)", name, instance.Status)
	}
	ip := resolveIP(instance)
	if ip == "" {
		return "", fmt.Errorf("%s has no IP address", name)
	}
	return ip, nil
}

// sshAsRoot runs a command on the container as root via SSH.
func sshAsRoot(cmd *cobra.Command, ip string, command []string) (int, error) {
	return ssh.Exec(cmd.Context(), ip, "root", cfg.SSH.Key, command)
}

func runNetworkShow(cmd *cobra.Command, args []string) error {
	ip, err := resolveContainerIP(cmd, args[0])
	if err != nil {
		return err
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "Fetching egress rules for %s...\n", args[0])

	// Show domains file.
	code, err := sshAsRoot(cmd, ip, []string{"cat", "/etc/pixels-egress-domains", "2>/dev/null", "||", "echo", "(no egress policy configured)"})
	if err != nil {
		return err
	}
	if code != 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "(no egress policy configured)")
	}
	return nil
}

func runNetworkSet(cmd *cobra.Command, args []string) error {
	name, mode := args[0], args[1]

	if mode != "unrestricted" && mode != "agent" && mode != "allowlist" {
		return fmt.Errorf("invalid mode %q: must be unrestricted, agent, or allowlist", mode)
	}

	ip, err := resolveContainerIP(cmd, name)
	if err != nil {
		return err
	}

	if mode == "unrestricted" {
		// Remove egress rules.
		sshAsRoot(cmd, ip, []string{"nft", "flush", "ruleset"})
		sshAsRoot(cmd, ip, []string{"rm", "-f", "/etc/pixels-egress-domains", "/etc/nftables.conf"})
		fmt.Fprintf(cmd.OutOrStdout(), "Egress set to unrestricted for %s\n", name)
		return nil
	}

	domains := egress.ResolveDomains(mode, cfg.Network.Allow)
	domainContent := egress.DomainsFileContent(domains)

	// Write domains file.
	writeCmd := fmt.Sprintf("cat > /etc/pixels-egress-domains << 'PIXELS_EOF'\n%sPIXELS_EOF", domainContent)
	if code, err := sshAsRoot(cmd, ip, []string{"bash", "-c", writeCmd}); err != nil || code != 0 {
		return fmt.Errorf("writing domains file: exit %d, err %v", code, err)
	}

	// Write nftables.conf.
	nftCmd := fmt.Sprintf("cat > /etc/nftables.conf << 'PIXELS_EOF'\n%sPIXELS_EOF", egress.NftablesConf())
	if code, err := sshAsRoot(cmd, ip, []string{"bash", "-c", nftCmd}); err != nil || code != 0 {
		return fmt.Errorf("writing nftables.conf: exit %d, err %v", code, err)
	}

	// Run resolve script (write it first if not present).
	scriptCmd := fmt.Sprintf("cat > /usr/local/bin/pixels-resolve-egress.sh << 'PIXELS_EOF'\n%sPIXELS_EOF\nchmod 755 /usr/local/bin/pixels-resolve-egress.sh", egress.ResolveScript())
	if code, err := sshAsRoot(cmd, ip, []string{"bash", "-c", scriptCmd}); err != nil || code != 0 {
		return fmt.Errorf("writing resolve script: exit %d, err %v", code, err)
	}

	if code, err := sshAsRoot(cmd, ip, []string{"/usr/local/bin/pixels-resolve-egress.sh"}); err != nil || code != 0 {
		return fmt.Errorf("running resolve script: exit %d, err %v", code, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Egress set to %s for %s (%d domains)\n", mode, name, len(domains))
	return nil
}

func runNetworkAllow(cmd *cobra.Command, args []string) error {
	name, domain := args[0], args[1]

	ip, err := resolveContainerIP(cmd, name)
	if err != nil {
		return err
	}

	// Append domain to file.
	appendCmd := fmt.Sprintf("echo %q >> /etc/pixels-egress-domains", domain)
	if code, err := sshAsRoot(cmd, ip, []string{"bash", "-c", appendCmd}); err != nil || code != 0 {
		return fmt.Errorf("appending domain: exit %d, err %v", code, err)
	}

	// Re-resolve.
	if code, err := sshAsRoot(cmd, ip, []string{"/usr/local/bin/pixels-resolve-egress.sh"}); err != nil || code != 0 {
		return fmt.Errorf("reloading rules: exit %d, err %v", code, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Allowed %s for %s\n", domain, name)
	return nil
}

func runNetworkDeny(cmd *cobra.Command, args []string) error {
	name, domain := args[0], args[1]

	ip, err := resolveContainerIP(cmd, name)
	if err != nil {
		return err
	}

	// Remove domain from file (escape for sed).
	escaped := strings.ReplaceAll(domain, ".", "\\.")
	sedCmd := fmt.Sprintf("sed -i '/^%s$/d' /etc/pixels-egress-domains", escaped)
	if code, err := sshAsRoot(cmd, ip, []string{"bash", "-c", sedCmd}); err != nil || code != 0 {
		return fmt.Errorf("removing domain: exit %d, err %v", code, err)
	}

	// Re-resolve (full reload replaces all rules).
	if code, err := sshAsRoot(cmd, ip, []string{"/usr/local/bin/pixels-resolve-egress.sh"}); err != nil || code != 0 {
		return fmt.Errorf("reloading rules: exit %d, err %v", code, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Denied %s for %s\n", domain, name)
	return nil
}
```

**Step 2: Build to verify compilation**

Run: `go build ./...`
Expected: Success

**Step 3: Commit**

```bash
git add cmd/network.go
git commit -m "feat: add pixels network command with show/set/allow/deny subcommands"
```

---

### Task 6: Integration verification and final cleanup

**Step 1: Run full test suite**

Run: `go test ./...`
Expected: All PASS

**Step 2: Build the binary**

Run: `go build -o pixels .`
Expected: Success

**Step 3: Verify help output**

Run: `./pixels network --help`
Expected: Shows subcommands (show, set, allow, deny)

Run: `./pixels create --help`
Expected: Shows `--egress` flag

**Step 4: Commit any remaining changes**

Only if there are changes from cleanup. Otherwise skip.
