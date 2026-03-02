package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/provision"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "sessions <name>",
		Short: "List zmx sessions in a container",
		Args:  cobra.ExactArgs(1),
		RunE:  runSessions,
	})
}

func runSessions(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]

	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	if err := sb.Ready(ctx, name, 30*time.Second); err != nil {
		return fmt.Errorf("waiting for instance: %w", err)
	}

	out, err := sb.Output(ctx, name, []string{"sh", "-c", "unset XDG_RUNTIME_DIR && zmx list"})
	if err != nil {
		return fmt.Errorf("zmx not available on %s", name)
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		fmt.Fprintln(cmd.OutOrStdout(), "No sessions")
		return nil
	}

	sessions := provision.ParseSessions(raw)
	if len(sessions) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No sessions")
		return nil
	}

	tw := newTabWriter(cmd)
	fmt.Fprintln(tw, "SESSION\tSTATUS")
	for _, s := range sessions {
		status := "running"
		if s.EndedAt != "" {
			status = "exited"
		}
		fmt.Fprintf(tw, "%s\t%s\n", s.Name, status)
	}
	return tw.Flush()
}
