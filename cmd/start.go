package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "start <name>",
		Short: "Start a stopped pixel",
		Args:  cobra.ExactArgs(1),
		RunE:  runStart,
	})
}

func runStart(cmd *cobra.Command, args []string) error {
	name := args[0]

	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	ctx := cmd.Context()

	if err := sb.Start(ctx, name); err != nil {
		return err
	}

	if err := sb.Ready(ctx, name, 30*time.Second); err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Started %s (not yet ready)\n", name)
		return nil
	}

	// Poll for IP assignment (DHCP takes a moment after agent is ready).
	var ip string
	deadline := time.After(15 * time.Second)
	for ip == "" {
		select {
		case <-deadline:
			fmt.Fprintf(cmd.OutOrStdout(), "Started %s (no IP assigned)\n", name)
			return nil
		case <-time.After(time.Second):
		}
		inst, err := sb.Get(ctx, name)
		if err == nil && len(inst.Addresses) > 0 {
			ip = inst.Addresses[0]
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Started %s\n", name)
	fmt.Fprintf(cmd.OutOrStdout(), "  IP:  %s\n", ip)
	fmt.Fprintf(cmd.OutOrStdout(), "  SSH: ssh %s@%s\n", cfg.SSH.User, ip)
	return nil
}
