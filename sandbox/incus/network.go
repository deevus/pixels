package incus

import (
	"context"
	"fmt"
	"strings"

	"github.com/deevus/pixels/internal/egress"
	"github.com/deevus/pixels/sandbox"
)

// SetEgressMode sets the egress filtering mode for a container.
func (i *Incus) SetEgressMode(ctx context.Context, name string, mode sandbox.EgressMode) error {
	full := prefixed(name)

	switch mode {
	case sandbox.EgressUnrestricted:
		// Flush nftables.
		i.execSimple(ctx, full, []string{"nft", "flush", "ruleset"})

		// Remove egress files.
		i.execSimple(ctx, full, []string{"rm", "-f",
			"/etc/pixels-egress-domains",
			"/etc/pixels-egress-cidrs",
			"/etc/nftables.conf",
			"/usr/local/bin/pixels-resolve-egress.sh",
			"/usr/local/bin/safe-apt",
		})

		// Restore blanket sudoers.
		if err := i.pushFile(full, "/etc/sudoers.d/pixel", []byte(egress.SudoersUnrestricted()), 0o440); err != nil {
			return fmt.Errorf("writing unrestricted sudoers: %w", err)
		}
		// Remove restricted sudoers if present.
		i.execSimple(ctx, full, []string{"rm", "-f", "/etc/sudoers.d/pixel.restricted"})

		return nil

	case sandbox.EgressAgent, sandbox.EgressAllowlist:
		egressName := string(mode)
		domains := egress.ResolveDomains(egressName, i.cfg.allow)

		// Write domain list.
		if err := i.pushFile(full, "/etc/pixels-egress-domains", []byte(egress.DomainsFileContent(domains)), 0o644); err != nil {
			return fmt.Errorf("writing egress domains: %w", err)
		}

		// Write CIDRs if any.
		cidrs := egress.PresetCIDRs(egressName)
		if len(cidrs) > 0 {
			if err := i.pushFile(full, "/etc/pixels-egress-cidrs", []byte(egress.CIDRsFileContent(cidrs)), 0o644); err != nil {
				return fmt.Errorf("writing egress cidrs: %w", err)
			}
		}

		// Write nftables config.
		if err := i.pushFile(full, "/etc/nftables.conf", []byte(egress.NftablesConf()), 0o644); err != nil {
			return fmt.Errorf("writing nftables.conf: %w", err)
		}

		// Write resolve script.
		if err := i.pushFile(full, "/usr/local/bin/pixels-resolve-egress.sh", []byte(egress.ResolveScript()), 0o755); err != nil {
			return fmt.Errorf("writing resolve script: %w", err)
		}

		// Write safe-apt wrapper.
		if err := i.pushFile(full, "/usr/local/bin/safe-apt", []byte(egress.SafeAptScript()), 0o755); err != nil {
			return fmt.Errorf("writing safe-apt: %w", err)
		}

		// Write restricted sudoers.
		if err := i.pushFile(full, "/etc/sudoers.d/pixel", []byte(egress.SudoersRestricted()), 0o440); err != nil {
			return fmt.Errorf("writing restricted sudoers: %w", err)
		}

		// Install nftables and resolve domains.
		rc := i.execSimple(ctx, full, []string{"bash", "-c", "DEBIAN_FRONTEND=noninteractive apt-get install -y -o Dpkg::Options::=--force-confold nftables >/dev/null 2>&1"})
		if rc != 0 {
			return fmt.Errorf("installing nftables: exit code %d", rc)
		}

		rc = i.execSimple(ctx, full, []string{"/usr/local/bin/pixels-resolve-egress.sh"})
		if rc != 0 {
			return fmt.Errorf("resolving egress: exit code %d", rc)
		}

		return nil

	default:
		return fmt.Errorf("unknown egress mode %q", mode)
	}
}

// AllowDomain adds a domain to the egress allowlist and re-resolves.
func (i *Incus) AllowDomain(ctx context.Context, name, domain string) error {
	full := prefixed(name)

	// Ensure egress infrastructure exists.
	rc := i.execSimple(ctx, full, []string{"test", "-f", "/etc/pixels-egress-domains"})
	if rc != 0 {
		if err := i.SetEgressMode(ctx, name, sandbox.EgressAllowlist); err != nil {
			return fmt.Errorf("setting up egress infra: %w", err)
		}
	}

	// Read current domains.
	out, err := i.readFile(full, "/etc/pixels-egress-domains")
	if err != nil {
		return fmt.Errorf("reading domains: %w", err)
	}

	domains := parseDomains(string(out))

	// Check for duplicate.
	for _, d := range domains {
		if d == domain {
			return nil
		}
	}

	domains = append(domains, domain)

	// Write updated domains.
	if err := i.pushFile(full, "/etc/pixels-egress-domains", []byte(egress.DomainsFileContent(domains)), 0o644); err != nil {
		return fmt.Errorf("writing domains: %w", err)
	}

	// Re-resolve.
	i.execSimple(ctx, full, []string{"/usr/local/bin/pixels-resolve-egress.sh"})

	return nil
}

// DenyDomain removes a domain from the egress allowlist and re-resolves.
func (i *Incus) DenyDomain(ctx context.Context, name, domain string) error {
	full := prefixed(name)

	out, err := i.readFile(full, "/etc/pixels-egress-domains")
	if err != nil {
		return fmt.Errorf("reading domains: %w", err)
	}

	domains := parseDomains(string(out))
	var filtered []string
	found := false
	for _, d := range domains {
		if d == domain {
			found = true
			continue
		}
		filtered = append(filtered, d)
	}
	if !found {
		return fmt.Errorf("domain %q not in allowlist", domain)
	}

	if err := i.pushFile(full, "/etc/pixels-egress-domains", []byte(egress.DomainsFileContent(filtered)), 0o644); err != nil {
		return fmt.Errorf("writing domains: %w", err)
	}

	// Re-resolve.
	i.execSimple(ctx, full, []string{"/usr/local/bin/pixels-resolve-egress.sh"})

	return nil
}

// GetPolicy returns the current egress policy for an instance.
func (i *Incus) GetPolicy(ctx context.Context, name string) (*sandbox.Policy, error) {
	full := prefixed(name)

	rc := i.execSimple(ctx, full, []string{"test", "-f", "/etc/pixels-egress-domains"})
	if rc != 0 {
		return &sandbox.Policy{Mode: sandbox.EgressUnrestricted}, nil
	}

	out, err := i.readFile(full, "/etc/pixels-egress-domains")
	if err != nil {
		return nil, fmt.Errorf("reading domains: %w", err)
	}

	domains := parseDomains(string(out))
	return &sandbox.Policy{
		Mode:    sandbox.EgressAllowlist,
		Domains: domains,
	}, nil
}

// parseDomains splits newline-delimited domain content into a slice.
func parseDomains(content string) []string {
	var domains []string
	for _, line := range strings.Split(content, "\n") {
		d := strings.TrimSpace(line)
		if d != "" {
			domains = append(domains, d)
		}
	}
	return domains
}
