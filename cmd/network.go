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
	_, err = sshAsRoot(cmd, ip, []string{"bash", "-c", showCmd})
	return err
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
		// Remove egress rules and restore blanket sudo.
		sshAsRoot(cmd, ip, []string{"nft", "flush", "ruleset"})
		sshAsRoot(cmd, ip, []string{"rm", "-f", "/etc/pixels-egress-domains", "/etc/nftables.conf", "/usr/local/bin/pixels-resolve-egress.sh"})
		restoreSudo := fmt.Sprintf("cat > /etc/sudoers.d/pixel << 'PIXELS_EOF'\n%sPIXELS_EOF\nchmod 0440 /etc/sudoers.d/pixel", egress.SudoersUnrestricted())
		sshAsRoot(cmd, ip, []string{"bash", "-c", restoreSudo})
		fmt.Fprintf(cmd.OutOrStdout(), "Egress set to unrestricted for %s\n", name)
		return nil
	}

	// Ensure egress infrastructure exists (nftables, resolve script, etc.).
	if err := ensureEgressFiles(cmd, ip); err != nil {
		return err
	}

	domains := egress.ResolveDomains(mode, cfg.Network.Allow)
	domainContent := egress.DomainsFileContent(domains)

	// Write domains file.
	writeCmd := fmt.Sprintf("cat > /etc/pixels-egress-domains << 'PIXELS_EOF'\n%sPIXELS_EOF", domainContent)
	if code, err := sshAsRoot(cmd, ip, []string{"bash", "-c", writeCmd}); err != nil || code != 0 {
		return fmt.Errorf("writing domains file: exit %d, err %v", code, err)
	}

	// Resolve domains and load nftables rules.
	if code, err := sshAsRoot(cmd, ip, []string{"/usr/local/bin/pixels-resolve-egress.sh"}); err != nil || code != 0 {
		return fmt.Errorf("running resolve script: exit %d, err %v", code, err)
	}

	// Restrict sudoers.
	restrictSudo := fmt.Sprintf("cat > /etc/sudoers.d/pixel << 'PIXELS_EOF'\n%sPIXELS_EOF\nchmod 0440 /etc/sudoers.d/pixel", egress.SudoersRestricted())
	if code, err := sshAsRoot(cmd, ip, []string{"bash", "-c", restrictSudo}); err != nil || code != 0 {
		return fmt.Errorf("writing restricted sudoers: exit %d, err %v", code, err)
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

	// Ensure egress infrastructure exists (idempotent).
	if err := ensureEgressFiles(cmd, ip); err != nil {
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

	// Check that egress is configured before trying to remove a domain.
	checkCode, _ := sshAsRoot(cmd, ip, []string{"test", "-f", "/etc/pixels-egress-domains"})
	if checkCode != 0 {
		return fmt.Errorf("no egress policy configured on %s", name)
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

// ensureEgressFiles writes the nftables config and resolve script if not already
// present. This allows `network allow` to work on containers that were created
// without egress configured.
func ensureEgressFiles(cmd *cobra.Command, ip string) error {
	// Check if resolve script already exists.
	checkCode, _ := sshAsRoot(cmd, ip, []string{"test", "-f", "/usr/local/bin/pixels-resolve-egress.sh"})
	if checkCode == 0 {
		return nil // already provisioned
	}

	// Write nftables.conf.
	nftCmd := fmt.Sprintf("cat > /etc/nftables.conf << 'PIXELS_EOF'\n%sPIXELS_EOF", egress.NftablesConf())
	if code, err := sshAsRoot(cmd, ip, []string{"bash", "-c", nftCmd}); err != nil || code != 0 {
		return fmt.Errorf("writing nftables.conf: exit %d, err %v", code, err)
	}

	// Write resolve script.
	scriptCmd := fmt.Sprintf("cat > /usr/local/bin/pixels-resolve-egress.sh << 'PIXELS_EOF'\n%sPIXELS_EOF\nchmod 755 /usr/local/bin/pixels-resolve-egress.sh", egress.ResolveScript())
	if code, err := sshAsRoot(cmd, ip, []string{"bash", "-c", scriptCmd}); err != nil || code != 0 {
		return fmt.Errorf("writing resolve script: exit %d, err %v", code, err)
	}

	// Ensure nftables and dnsutils are installed.
	if code, err := sshAsRoot(cmd, ip, []string{"bash", "-c", "apt-get install -y -qq nftables dnsutils"}); err != nil || code != 0 {
		return fmt.Errorf("installing nftables: exit %d, err %v", code, err)
	}

	// Create empty domains file if it doesn't exist.
	sshAsRoot(cmd, ip, []string{"bash", "-c", "touch /etc/pixels-egress-domains"})

	return nil
}
