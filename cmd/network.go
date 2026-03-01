package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/cache"
	"github.com/deevus/pixels/internal/egress"
	"github.com/deevus/pixels/internal/ssh"
	tnc "github.com/deevus/pixels/internal/truenas"
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

// networkContext holds the resolved state needed by network subcommands.
type networkContext struct {
	name   string
	ip     string
	client *tnc.Client
}

// resolveNetworkContext connects to TrueNAS and resolves the container's IP.
func resolveNetworkContext(cmd *cobra.Command, name string) (*networkContext, error) {
	ctx := cmd.Context()

	// Try cache for IP.
	var ip string
	if entry := cache.Get(name); entry != nil && entry.Status == "RUNNING" && entry.IP != "" {
		ip = entry.IP
	}

	client, err := connectClient(ctx)
	if err != nil {
		return nil, err
	}

	if ip == "" {
		instance, err := client.Virt.GetInstance(ctx, containerName(name))
		if err != nil {
			client.Close()
			return nil, fmt.Errorf("looking up %s: %w", name, err)
		}
		if instance.Status != "RUNNING" {
			client.Close()
			return nil, fmt.Errorf("%s is not running (status: %s)", name, instance.Status)
		}
		ip = resolveIP(instance)
		if ip == "" {
			client.Close()
			return nil, fmt.Errorf("%s has no IP address", name)
		}
	}

	return &networkContext{name: name, ip: ip, client: client}, nil
}

// sshAsRoot runs a command on the container as root via SSH.
func sshAsRoot(cmd *cobra.Command, ip string, command []string) (int, error) {
	return ssh.Exec(cmd.Context(), ip, "root", cfg.SSH.Key, command, nil)
}

func runNetworkShow(cmd *cobra.Command, args []string) error {
	nc, err := resolveNetworkContext(cmd, args[0])
	if err != nil {
		return err
	}
	defer nc.client.Close()

	fmt.Fprintf(cmd.ErrOrStderr(), "Fetching egress rules for %s...\n", args[0])

	// Show domains and rule count via a single shell command.
	showCmd := `if [ -f /etc/pixels-egress-domains ]; then
    echo "Mode: restricted"
    echo "Domains:"
    sed 's/^/  /' /etc/pixels-egress-domains
    count=$(nft list set inet pixels_egress allowed_v4 2>/dev/null | grep -c 'elements' || echo 0)
    echo "Resolved IPs: $count"
else
    echo "Mode: unrestricted (no egress policy configured)"
fi`
	_, err = sshAsRoot(cmd, nc.ip, []string{"bash", "-c", showCmd})
	return err
}

func runNetworkSet(cmd *cobra.Command, args []string) error {
	name, mode := args[0], args[1]

	if mode != "unrestricted" && mode != "agent" && mode != "allowlist" {
		return fmt.Errorf("invalid mode %q: must be unrestricted, agent, or allowlist", mode)
	}

	nc, err := resolveNetworkContext(cmd, name)
	if err != nil {
		return err
	}
	defer nc.client.Close()
	ctx := cmd.Context()
	cname := containerName(name)

	if mode == "unrestricted" {
		// Remove egress rules and restore blanket sudo.
		sshAsRoot(cmd, nc.ip, []string{"nft", "flush", "ruleset"})
		sshAsRoot(cmd, nc.ip, []string{"rm", "-f", "/etc/pixels-egress-domains", "/etc/nftables.conf", "/usr/local/bin/pixels-resolve-egress.sh"})
		restoreSudo := fmt.Sprintf("cat > /etc/sudoers.d/pixel << 'PIXELS_EOF'\n%sPIXELS_EOF\nchmod 0440 /etc/sudoers.d/pixel", egress.SudoersUnrestricted())
		sshAsRoot(cmd, nc.ip, []string{"bash", "-c", restoreSudo})
		fmt.Fprintf(cmd.OutOrStdout(), "Egress set to unrestricted for %s\n", name)
		return nil
	}

	// Always write nftables.conf and resolve script — ensures the latest
	// rules are applied when switching modes or after binary updates.
	if err := writeEgressInfra(cmd, nc.ip, nc.client, cname); err != nil {
		return err
	}

	domains := egress.ResolveDomains(mode, cfg.Network.Allow)

	// Write domains file via TrueNAS API.
	if err := nc.client.WriteContainerFile(ctx, cname, "/etc/pixels-egress-domains", []byte(egress.DomainsFileContent(domains)), 0o644); err != nil {
		return fmt.Errorf("writing domains file: %w", err)
	}

	// Write CIDRs file if the preset has any.
	cidrs := egress.PresetCIDRs(mode)
	if len(cidrs) > 0 {
		if err := nc.client.WriteContainerFile(ctx, cname, "/etc/pixels-egress-cidrs", []byte(egress.CIDRsFileContent(cidrs)), 0o644); err != nil {
			return fmt.Errorf("writing cidrs file: %w", err)
		}
	}

	// Resolve domains and load nftables rules.
	if code, err := sshAsRoot(cmd, nc.ip, []string{"/usr/local/bin/pixels-resolve-egress.sh"}); err != nil || code != 0 {
		return fmt.Errorf("running resolve script: exit %d, err %v", code, err)
	}

	// Write safe-apt wrapper and restrict sudoers.
	if err := nc.client.WriteContainerFile(ctx, cname, "/usr/local/bin/safe-apt", []byte(egress.SafeAptScript()), 0o755); err != nil {
		return fmt.Errorf("writing safe-apt wrapper: %w", err)
	}
	if err := nc.client.WriteContainerFile(ctx, cname, "/etc/sudoers.d/pixel", []byte(egress.SudoersRestricted()), 0o440); err != nil {
		return fmt.Errorf("writing restricted sudoers: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Egress set to %s for %s (%d domains)\n", mode, name, len(domains))
	return nil
}

func runNetworkAllow(cmd *cobra.Command, args []string) error {
	name, domain := args[0], args[1]

	nc, err := resolveNetworkContext(cmd, name)
	if err != nil {
		return err
	}
	defer nc.client.Close()
	ctx := cmd.Context()
	cname := containerName(name)

	// Ensure egress infrastructure exists (idempotent).
	if err := ensureEgressFiles(cmd, nc.ip, nc.client, cname); err != nil {
		return err
	}

	// Read current domains via SSH.
	out, err := ssh.Output(ctx, nc.ip, "root", cfg.SSH.Key, []string{"cat", "/etc/pixels-egress-domains"})
	if err != nil {
		return fmt.Errorf("reading domains file: %w", err)
	}

	// Append domain if not already present.
	current := strings.TrimSpace(string(out))
	lines := strings.Split(current, "\n")
	for _, l := range lines {
		if strings.TrimSpace(l) == domain {
			fmt.Fprintf(cmd.OutOrStdout(), "%s already allowed for %s\n", domain, name)
			return nil
		}
	}
	if current != "" {
		current += "\n"
	}
	current += domain + "\n"

	// Write back via TrueNAS API.
	if err := nc.client.WriteContainerFile(ctx, cname, "/etc/pixels-egress-domains", []byte(current), 0o644); err != nil {
		return fmt.Errorf("writing domains file: %w", err)
	}

	// Re-resolve.
	if code, err := sshAsRoot(cmd, nc.ip, []string{"/usr/local/bin/pixels-resolve-egress.sh"}); err != nil || code != 0 {
		return fmt.Errorf("reloading rules: exit %d, err %v", code, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Allowed %s for %s\n", domain, name)
	return nil
}

func runNetworkDeny(cmd *cobra.Command, args []string) error {
	name, domain := args[0], args[1]

	nc, err := resolveNetworkContext(cmd, name)
	if err != nil {
		return err
	}
	defer nc.client.Close()
	ctx := cmd.Context()
	cname := containerName(name)

	// Read current domains via SSH.
	out, err := ssh.Output(ctx, nc.ip, "root", cfg.SSH.Key, []string{"cat", "/etc/pixels-egress-domains"})
	if err != nil {
		return fmt.Errorf("no egress policy configured on %s", name)
	}

	// Remove domain.
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var kept []string
	found := false
	for _, l := range lines {
		if strings.TrimSpace(l) == domain {
			found = true
			continue
		}
		kept = append(kept, l)
	}
	if !found {
		return fmt.Errorf("domain %s not found in egress allowlist for %s", domain, name)
	}

	content := strings.Join(kept, "\n") + "\n"

	// Write back via TrueNAS API.
	if err := nc.client.WriteContainerFile(ctx, cname, "/etc/pixels-egress-domains", []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing domains file: %w", err)
	}

	// Re-resolve (full reload replaces all rules).
	if code, err := sshAsRoot(cmd, nc.ip, []string{"/usr/local/bin/pixels-resolve-egress.sh"}); err != nil || code != 0 {
		return fmt.Errorf("reloading rules: exit %d, err %v", code, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Denied %s for %s\n", domain, name)
	return nil
}

// writeEgressInfra writes nftables.conf and the resolve script unconditionally.
// Called by `network set` to ensure the latest rules are always applied.
func writeEgressInfra(cmd *cobra.Command, ip string, client *tnc.Client, cname string) error {
	ctx := cmd.Context()

	// Write nftables.conf via TrueNAS API.
	if err := client.WriteContainerFile(ctx, cname, "/etc/nftables.conf", []byte(egress.NftablesConf()), 0o644); err != nil {
		return fmt.Errorf("writing nftables.conf: %w", err)
	}

	// Write resolve script via TrueNAS API.
	if err := client.WriteContainerFile(ctx, cname, "/usr/local/bin/pixels-resolve-egress.sh", []byte(egress.ResolveScript()), 0o755); err != nil {
		return fmt.Errorf("writing resolve script: %w", err)
	}

	// Ensure nftables and dnsutils are installed. Use confold to keep our
	// pre-written /etc/nftables.conf and avoid dpkg conffile prompts.
	sshAsRoot(cmd, ip, []string{"bash", "-c", `DEBIAN_FRONTEND=noninteractive apt-get install -y -qq -o Dpkg::Options::="--force-confold" nftables dnsutils`})

	return nil
}

// ensureEgressFiles writes the nftables config and resolve script if not already
// present. This allows `network allow` to work on containers that were created
// without egress configured.
func ensureEgressFiles(cmd *cobra.Command, ip string, client *tnc.Client, cname string) error {
	checkCode, _ := sshAsRoot(cmd, ip, []string{"test", "-f", "/usr/local/bin/pixels-resolve-egress.sh"})
	if checkCode == 0 {
		return nil // already provisioned
	}
	if err := writeEgressInfra(cmd, ip, client, cname); err != nil {
		return err
	}
	// Create empty domains file so allow can append to it.
	sshAsRoot(cmd, ip, []string{"touch", "/etc/pixels-egress-domains"})
	return nil
}
