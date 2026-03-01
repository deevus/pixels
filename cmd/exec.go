package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/ssh"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "exec <name> -- <command...>",
		Short: "Run a command in a pixel via SSH",
		Args:  cobra.MinimumNArgs(2),
		RunE:  runExec,
	})
}

func runExec(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	command := args[1:]

	ip, err := resolveRunningIP(ctx, name)
	if err != nil {
		return err
	}

	if err := ssh.WaitReady(ctx, ip, 30*time.Second, nil); err != nil {
		return fmt.Errorf("waiting for SSH: %w", err)
	}

	// Verify key auth; if it fails, write this machine's key via TrueNAS.
	if err := ensureSSHAuth(cmd, ctx, ip, name); err != nil {
		return err
	}

	exitCode, err := ssh.Exec(ctx, ip, cfg.SSH.User, cfg.SSH.Key, command, cfg.EnvForward)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
	return nil
}
