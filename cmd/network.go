package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/deevus/pixels/sandbox"
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

func runNetworkShow(cmd *cobra.Command, args []string) error {
	name := args[0]

	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	policy, err := sb.GetPolicy(cmd.Context(), name)
	if err != nil {
		return err
	}

	if policy.Mode == sandbox.EgressUnrestricted {
		fmt.Fprintln(cmd.OutOrStdout(), "Mode: unrestricted")
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Mode: %s\n", policy.Mode)
	if len(policy.Domains) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Domains:")
		for _, d := range policy.Domains {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", d)
		}
	}
	return nil
}

func runNetworkSet(cmd *cobra.Command, args []string) error {
	name, mode := args[0], args[1]

	if mode != "unrestricted" && mode != "agent" && mode != "allowlist" {
		return fmt.Errorf("invalid mode %q: must be unrestricted, agent, or allowlist", mode)
	}

	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	if err := sb.SetEgressMode(cmd.Context(), name, sandbox.EgressMode(mode)); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Egress set to %s for %s\n", mode, name)
	return nil
}

func runNetworkAllow(cmd *cobra.Command, args []string) error {
	name, domain := args[0], args[1]

	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	if err := sb.AllowDomain(cmd.Context(), name, domain); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Allowed %s for %s\n", domain, name)
	return nil
}

func runNetworkDeny(cmd *cobra.Command, args []string) error {
	name, domain := args[0], args[1]

	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	if err := sb.DenyDomain(cmd.Context(), name, domain); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Denied %s for %s\n", domain, name)
	return nil
}
