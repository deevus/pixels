package truenas

import (
	"context"
	"fmt"
	"strings"

	"github.com/deevus/pixels/internal/egress"
	"github.com/deevus/pixels/internal/ssh"
	"github.com/deevus/pixels/sandbox"
)

// SetEgressMode sets the egress filtering mode for a container.
//
// For "unrestricted": flushes nftables, removes egress files, restores
// blanket sudoers.
//
// For "agent"/"allowlist": writes nftables config, domains/cidrs, resolve
// script, safe-apt wrapper, restricted sudoers via the TrueNAS API, then
// SSHes in to install nftables and resolve domains.
func (t *TrueNAS) SetEgressMode(ctx context.Context, name string, mode sandbox.EgressMode) error {
	ip, err := t.resolveRunningIP(ctx, name)
	if err != nil {
		return err
	}
	cc := ssh.ConnConfig{Host: ip, User: "root", KeyPath: t.cfg.sshKey}
	full := prefixed(name)

	switch mode {
	case sandbox.EgressUnrestricted:
		// Flush nftables.
		t.ssh.ExecQuiet(ctx, cc, []string{"nft flush ruleset"})

		// Remove egress files.
		t.ssh.ExecQuiet(ctx, cc, []string{"rm -f /etc/pixels-egress-domains /etc/pixels-egress-cidrs /etc/nftables.conf /usr/local/bin/pixels-resolve-egress.sh /usr/local/bin/safe-apt"})

		// Restore blanket sudoers.
		if err := t.client.WriteContainerFile(ctx, full, "/etc/sudoers.d/pixel", []byte(egress.SudoersUnrestricted()), 0o440); err != nil {
			return fmt.Errorf("writing unrestricted sudoers: %w", err)
		}
		// Remove restricted sudoers if present.
		t.ssh.ExecQuiet(ctx, cc, []string{"rm -f /etc/sudoers.d/pixel.restricted"})

		return nil

	case sandbox.EgressAgent, sandbox.EgressAllowlist:
		egressName := string(mode)
		domains := egress.ResolveDomains(egressName, t.cfg.allow)

		// Write domain list.
		if err := t.client.WriteContainerFile(ctx, full, "/etc/pixels-egress-domains", []byte(egress.DomainsFileContent(domains)), 0o644); err != nil {
			return fmt.Errorf("writing egress domains: %w", err)
		}

		// Write CIDRs if any.
		cidrs := egress.PresetCIDRs(egressName)
		if len(cidrs) > 0 {
			if err := t.client.WriteContainerFile(ctx, full, "/etc/pixels-egress-cidrs", []byte(egress.CIDRsFileContent(cidrs)), 0o644); err != nil {
				return fmt.Errorf("writing egress cidrs: %w", err)
			}
		}

		// Write nftables config.
		if err := t.client.WriteContainerFile(ctx, full, "/etc/nftables.conf", []byte(egress.NftablesConf()), 0o644); err != nil {
			return fmt.Errorf("writing nftables.conf: %w", err)
		}

		// Write resolve script.
		if err := t.client.WriteContainerFile(ctx, full, "/usr/local/bin/pixels-resolve-egress.sh", []byte(egress.ResolveScript()), 0o755); err != nil {
			return fmt.Errorf("writing resolve script: %w", err)
		}

		// Write safe-apt wrapper.
		if err := t.client.WriteContainerFile(ctx, full, "/usr/local/bin/safe-apt", []byte(egress.SafeAptScript()), 0o755); err != nil {
			return fmt.Errorf("writing safe-apt: %w", err)
		}

		// Write restricted sudoers.
		if err := t.client.WriteContainerFile(ctx, full, "/etc/sudoers.d/pixel", []byte(egress.SudoersRestricted()), 0o440); err != nil {
			return fmt.Errorf("writing restricted sudoers: %w", err)
		}

		// Install nftables and resolve domains via SSH.
		code, err := t.ssh.ExecQuiet(ctx, cc, []string{"apt-get install -y nftables >/dev/null 2>&1"})
		if err != nil {
			return fmt.Errorf("installing nftables: %w", err)
		}
		if code != 0 {
			return fmt.Errorf("installing nftables: exit code %d", code)
		}

		code, err = t.ssh.ExecQuiet(ctx, cc, []string{"/usr/local/bin/pixels-resolve-egress.sh"})
		if err != nil {
			return fmt.Errorf("resolving egress: %w", err)
		}
		if code != 0 {
			return fmt.Errorf("resolving egress: exit code %d", code)
		}

		return nil

	default:
		return fmt.Errorf("unknown egress mode %q", mode)
	}
}

// AllowDomain adds a domain to the egress allowlist and re-resolves.
func (t *TrueNAS) AllowDomain(ctx context.Context, name, domain string) error {
	ip, err := t.resolveRunningIP(ctx, name)
	if err != nil {
		return err
	}
	cc := ssh.ConnConfig{Host: ip, User: "root", KeyPath: t.cfg.sshKey}
	full := prefixed(name)

	// Ensure egress infrastructure exists.
	code, _ := t.ssh.ExecQuiet(ctx, cc, []string{"test -f /etc/pixels-egress-domains"})
	if code != 0 {
		// No egress infra — set up allowlist mode first.
		if err := t.SetEgressMode(ctx, name, sandbox.EgressAllowlist); err != nil {
			return fmt.Errorf("setting up egress infra: %w", err)
		}
	}

	// Read current domains.
	out, err := t.ssh.OutputQuiet(ctx, cc, []string{"cat /etc/pixels-egress-domains"})
	if err != nil {
		return fmt.Errorf("reading domains: %w", err)
	}

	domains := parseDomains(string(out))

	// Check for duplicate.
	for _, d := range domains {
		if d == domain {
			return nil // already allowed
		}
	}

	domains = append(domains, domain)

	// Write updated domains via API.
	if err := t.client.WriteContainerFile(ctx, full, "/etc/pixels-egress-domains", []byte(egress.DomainsFileContent(domains)), 0o644); err != nil {
		return fmt.Errorf("writing domains: %w", err)
	}

	// Re-resolve.
	t.ssh.ExecQuiet(ctx, cc, []string{"/usr/local/bin/pixels-resolve-egress.sh"})

	return nil
}

// DenyDomain removes a domain from the egress allowlist and re-resolves.
func (t *TrueNAS) DenyDomain(ctx context.Context, name, domain string) error {
	ip, err := t.resolveRunningIP(ctx, name)
	if err != nil {
		return err
	}
	cc := ssh.ConnConfig{Host: ip, User: "root", KeyPath: t.cfg.sshKey}
	full := prefixed(name)

	out, err := t.ssh.OutputQuiet(ctx, cc, []string{"cat /etc/pixels-egress-domains"})
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

	if err := t.client.WriteContainerFile(ctx, full, "/etc/pixels-egress-domains", []byte(egress.DomainsFileContent(filtered)), 0o644); err != nil {
		return fmt.Errorf("writing domains: %w", err)
	}

	// Re-resolve.
	t.ssh.ExecQuiet(ctx, cc, []string{"/usr/local/bin/pixels-resolve-egress.sh"})

	return nil
}

// GetPolicy returns the current egress policy for an instance.
func (t *TrueNAS) GetPolicy(ctx context.Context, name string) (*sandbox.Policy, error) {
	ip, err := t.resolveRunningIP(ctx, name)
	if err != nil {
		return nil, err
	}
	cc := ssh.ConnConfig{Host: ip, User: "root", KeyPath: t.cfg.sshKey}

	code, _ := t.ssh.ExecQuiet(ctx, cc, []string{"test -f /etc/pixels-egress-domains"})
	if code != 0 {
		return &sandbox.Policy{Mode: sandbox.EgressUnrestricted}, nil
	}

	out, err := t.ssh.OutputQuiet(ctx, cc, []string{"cat /etc/pixels-egress-domains"})
	if err != nil {
		return nil, fmt.Errorf("reading domains: %w", err)
	}

	domains := parseDomains(string(out))
	return &sandbox.Policy{
		Mode:    sandbox.EgressAllowlist,
		Domains: domains,
	}, nil
}

// parseDomains splits newline-delimited domain content into a slice,
// trimming whitespace and skipping empty lines.
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
