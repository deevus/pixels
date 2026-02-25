package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/ssh"
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
	ctx := cmd.Context()
	name := args[0]

	client, err := connectClient(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.Virt.StartInstance(ctx, containerName(name)); err != nil {
		return fmt.Errorf("starting %s: %w", name, err)
	}

	// Re-fetch to get the IP.
	instance, err := client.Virt.GetInstance(ctx, containerName(name))
	if err != nil {
		return fmt.Errorf("refreshing %s: %w", name, err)
	}

	ip := resolveIP(instance)
	if ip != "" {
		if err := ssh.WaitReady(ctx, ip, 30*time.Second); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: SSH not ready: %v\n", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Started %s\n", name)
		fmt.Fprintf(cmd.OutOrStdout(), "  IP:  %s\n", ip)
		fmt.Fprintf(cmd.OutOrStdout(), "  SSH: ssh %s@%s\n", cfg.SSH.User, ip)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Started %s (no IP assigned)\n", name)
	}
	return nil
}
