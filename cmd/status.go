package cmd

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/provision"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "status <name>",
		Short: "Show provisioning step status",
		Args:  cobra.ExactArgs(1),
		RunE:  runStatus,
	})
}

func runStatus(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]

	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	if err := sb.Ready(ctx, name, 10*time.Second); err != nil {
		return fmt.Errorf("waiting for instance: %w", err)
	}

	runner := provision.NewRunnerWith(&sandboxExecutor{sb: sb, name: name})
	raw, err := runner.List(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "No such file") {
			fmt.Fprintln(cmd.OutOrStdout(), "No provisioning steps found (zmx not installed)")
			return nil
		}
		return err
	}

	sessions := provision.ParseSessions(raw)

	// Filter to px-* sessions (our provisioning steps).
	var steps []provision.Session
	for _, s := range sessions {
		if strings.HasPrefix(s.Name, "px-") {
			steps = append(steps, s)
		}
	}

	if len(steps) == 0 {
		if runner.IsProvisioned(ctx) {
			fmt.Fprintln(cmd.OutOrStdout(), "Provisioning complete")
		} else if runner.HasProvisionScript(ctx) {
			fmt.Fprintln(cmd.OutOrStdout(), "Provisioning in progress...")
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "No provisioning steps found")
		}
		return nil
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "STEP\tSTATUS\tEXIT")
	for _, s := range steps {
		status := "running"
		exit := "-"
		if s.EndedAt != "" {
			status = "done"
			exit = s.ExitCode
			if exit != "0" {
				status = "failed"
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", s.Name, status, exit)
	}
	w.Flush()

	return nil
}
