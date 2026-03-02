package cmd

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/briandowns/spinner"
	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/provision"
	"github.com/deevus/pixels/sandbox"
)

var validSessionName = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func init() {
	cmd := &cobra.Command{
		Use:   "console <name>",
		Short: "Open a persistent session (zmx)",
		Args:  cobra.ExactArgs(1),
		RunE:  runConsole,
	}
	cmd.Flags().StringP("session", "s", "console", "zmx session name")
	cmd.Flags().Bool("no-persist", false, "skip zmx, use plain shell")
	rootCmd.AddCommand(cmd)
}

func runConsole(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]

	session, _ := cmd.Flags().GetString("session")
	noPersist, _ := cmd.Flags().GetBool("no-persist")

	if !noPersist && !validSessionName.MatchString(session) {
		return fmt.Errorf("invalid session name %q: must match [a-zA-Z0-9._-]", session)
	}

	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	inst, err := sb.Get(ctx, name)
	if err != nil {
		return fmt.Errorf("looking up %s: %w", name, err)
	}

	if !inst.Status.IsRunning() {
		fmt.Fprintf(cmd.ErrOrStderr(), "Starting %s...\n", name)
		if err := sb.Start(ctx, name); err != nil {
			return fmt.Errorf("starting instance: %w", err)
		}
	}

	if err := sb.Ready(ctx, name, 30*time.Second); err != nil {
		return fmt.Errorf("waiting for instance: %w", err)
	}

	// Wait for provisioning to finish before opening the console.
	runner := provision.NewRunnerWith(&sandboxExecutor{sb: sb, name: name})
	var spin *spinner.Spinner
	if !verbose {
		spin = spinner.New(spinner.CharSets[14], 100*time.Millisecond, spinner.WithWriter(cmd.ErrOrStderr()))
	}
	runner.WaitProvisioned(ctx, func(status string) {
		if spin != nil {
			spin.Suffix = "  " + status
			if !spin.Active() {
				spin.Start()
			}
		} else {
			logv(cmd, "Provision: %s", status)
		}
	})
	if spin != nil && spin.Active() {
		spin.Stop()
	}

	// Determine remote command for zmx session persistence.
	var remoteCmd []string
	if !noPersist {
		remoteCmd = zmxRemoteCmdViaSandbox(ctx, sb, name, session)
	}

	var envSlice []string
	for k, v := range cfg.EnvForward {
		envSlice = append(envSlice, k+"="+v)
	}

	return sb.Console(ctx, name, sandbox.ConsoleOpts{
		Env:       envSlice,
		RemoteCmd: remoteCmd,
	})
}

// zmxRemoteCmdViaSandbox checks if zmx is available in the container and returns
// the attach command. Returns nil if zmx is not installed.
func zmxRemoteCmdViaSandbox(ctx context.Context, sb sandbox.Sandbox, name, session string) []string {
	code, err := sb.Run(ctx, name, sandbox.ExecOpts{
		Cmd: []string{"sh", "-c", "command -v zmx >/dev/null 2>&1"},
	})
	if err == nil && code == 0 {
		return []string{"sh", "-lc", "unset XDG_RUNTIME_DIR && zmx attach " + session + " bash -l"}
	}
	return nil
}
