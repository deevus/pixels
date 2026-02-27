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
		// Git + GitHub release CDN
		"github.com",
		"api.github.com",
		"gitlab.com",
		"objects.githubusercontent.com",
		"raw.githubusercontent.com",
		"codeload.github.com",
		"github-releases.githubusercontent.com",
		"tmaproduction.blob.core.windows.net",
		// Sigstore (GitHub release attestation verification)
		"tuf-repo-cdn.sigstore.dev",
		// SDK / tool downloads
		"dl.google.com",
		// Tools
		"mise.run",
		"mise.jdx.dev",
		"nodejs.org",
		// Ubuntu package repos (needed for apt-get after egress rules are loaded)
		"archive.ubuntu.com",
		"security.ubuntu.com",
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
    /usr/bin/journalctl, /usr/bin/journalctl *, \
    /usr/bin/test, \
    /usr/sbin/nft list *
`
}

// SudoersUnrestricted returns the blanket sudoers content (current behavior).
func SudoersUnrestricted() string {
	return "pixel ALL=(ALL) NOPASSWD: ALL\n"
}
