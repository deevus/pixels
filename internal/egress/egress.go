package egress

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

//go:embed presets.toml
var presetsFile string

type preset struct {
	Domains []string `toml:"domains"`
	CIDRs   []string `toml:"cidrs"`
}

var presets map[string]preset

func init() {
	if _, err := toml.Decode(presetsFile, &presets); err != nil {
		panic(fmt.Sprintf("parsing egress presets.toml: %v", err))
	}
}

// PresetDomains returns the domain allowlist for a named preset.
// Returns nil if the preset doesn't exist.
func PresetDomains(name string) []string {
	if p, ok := presets[name]; ok {
		return p.Domains
	}
	return nil
}

// PresetCIDRs returns the CIDR ranges for a named preset.
// Returns nil if the preset doesn't exist or has no CIDRs.
func PresetCIDRs(name string) []string {
	if p, ok := presets[name]; ok {
		return p.CIDRs
	}
	return nil
}

// ResolveDomains returns the final domain list for the given egress mode.
// Returns nil for "unrestricted".
func ResolveDomains(egress string, allow []string) []string {
	switch egress {
	case "unrestricted":
		return nil
	case "allowlist":
		return allow
	default:
		// Treat as preset name (e.g. "agent").
		seen := make(map[string]bool)
		var merged []string
		for _, d := range PresetDomains(egress) {
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
	}
}

// DomainsFileContent returns the content of /etc/pixels-egress-domains.
func DomainsFileContent(domains []string) string {
	return strings.Join(domains, "\n") + "\n"
}

// CIDRsFileContent returns the content of /etc/pixels-egress-cidrs.
func CIDRsFileContent(cidrs []string) string {
	if len(cidrs) == 0 {
		return ""
	}
	return strings.Join(cidrs, "\n") + "\n"
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

// ResolveScript returns the shell script that reads /etc/pixels-egress-domains
// and /etc/pixels-egress-cidrs, and populates the nftables allowed_v4 set.
func ResolveScript() string {
	return `#!/bin/bash
set -euo pipefail

DOMAIN_FILE="/etc/pixels-egress-domains"
CIDR_FILE="/etc/pixels-egress-cidrs"
NFT_CONF="/etc/nftables.conf"

if [ ! -f "$DOMAIN_FILE" ]; then
    echo "No domain file found, skipping egress setup"
    exit 0
fi

# Load the base ruleset (creates table and empty set).
nft -f "$NFT_CONF"

# Add CIDR ranges first (CDN providers with rotating IPs).
if [ -f "$CIDR_FILE" ]; then
    while IFS= read -r cidr || [ -n "$cidr" ]; do
        cidr=$(echo "$cidr" | xargs)
        [ -z "$cidr" ] && continue
        [[ "$cidr" == \#* ]] && continue
        nft add element inet pixels_egress allowed_v4 "{ $cidr }" 2>/dev/null || true
    done < "$CIDR_FILE"
fi

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

echo "Egress rules loaded"
`
}

// SafeAptScript returns a wrapper script that sanitizes apt-get arguments,
// blocking -o flags (which allow arbitrary command execution via Pre-Invoke)
// and restricting to safe subcommands.
func SafeAptScript() string {
	return `#!/bin/bash
# Safe apt wrapper — blocks -o flags to prevent Pre-Invoke escalation.
set -eu

allowed_cmds="install|update|remove|autoremove|upgrade|dist-upgrade|purge"

if [ $# -lt 1 ]; then
    echo "Usage: safe-apt <$allowed_cmds> [packages...]" >&2
    exit 1
fi

cmd="$1"
shift

if ! echo "$cmd" | grep -qE "^($allowed_cmds)$"; then
    echo "safe-apt: '$cmd' is not allowed (use: $allowed_cmds)" >&2
    exit 1
fi

# Block dangerous flags.
for arg in "$@"; do
    case "$arg" in
        -o|--option|-o=*|--option=*)
            echo "safe-apt: -o/--option flags are not allowed" >&2
            exit 1
            ;;
    esac
done

export DEBIAN_FRONTEND=noninteractive
exec /usr/bin/apt-get "$cmd" "$@"
`
}

// SudoersRestricted returns the sudoers content for restricted egress mode.
func SudoersRestricted() string {
	return `pixel ALL=(ALL) NOPASSWD: /usr/local/bin/safe-apt, \
    /usr/bin/dpkg-query, \
    /usr/bin/systemctl start *, /usr/bin/systemctl stop *, \
    /usr/bin/systemctl restart *, /usr/bin/systemctl status *, \
    /usr/bin/systemctl enable *, /usr/bin/systemctl disable *, \
    /usr/bin/journalctl, /usr/bin/journalctl *, \
    /usr/bin/test, \
    /usr/sbin/nft list *
`
}

// SudoersUnrestricted returns the blanket sudoers content (current behavior).
func SudoersUnrestricted() string {
	return "pixel ALL=(ALL) NOPASSWD: ALL\n"
}
