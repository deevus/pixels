package cmd

import (
	"fmt"
	"os"
	"time"

	"al.essio.dev/pkg/shellescape"
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

	// Wrap in a login shell so ~/.profile is sourced (adds ~/.local/bin to PATH).
	// Activate mise if installed so tools it manages (claude, node, etc.) are on PATH.
	// Pass as a single string so SSH's argument concatenation preserves quoting.
	inner := shellescape.QuoteCommand(command)
	loginCmd := []string{"bash -lc " + shellescape.Quote("eval \"$(mise activate bash 2>/dev/null)\"; "+inner)}
	exitCode, err := ssh.Exec(ctx, ssh.ConnConfig{Host: ip, User: cfg.SSH.User, KeyPath: cfg.SSH.Key, Env: cfg.EnvForward}, loginCmd)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
	return nil
}
