package cmd

import (
	"os"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/spf13/cobra"

	"github.com/deevus/pixels/sandbox"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "exec <name> -- <command...>",
		Short: "Run a command in a pixel",
		Args:  cobra.MinimumNArgs(2),
		RunE:  runExec,
	})
}

func runExec(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	command := args[1:]

	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	if err := sb.Ready(ctx, name, 30*time.Second); err != nil {
		return err
	}

	// Wrap in a login shell so ~/.profile is sourced (adds ~/.local/bin to PATH).
	// Activate mise if installed so tools it manages (claude, node, etc.) are on PATH.
	// Pass as a single string so argument concatenation preserves quoting.
	inner := shellescape.QuoteCommand(command)
	loginCmd := []string{"bash", "-lc", "eval \"$(mise activate bash 2>/dev/null)\"; " + inner}

	var envSlice []string
	for k, v := range cfg.EnvForward {
		envSlice = append(envSlice, k+"="+v)
	}

	exitCode, err := sb.Run(ctx, name, sandbox.ExecOpts{
		Cmd:    loginCmd,
		Env:    envSlice,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	})
	if err != nil {
		return err
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
	return nil
}
