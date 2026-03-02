package cmd

import (
	"fmt"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/spf13/cobra"
)

func init() {
	cpCmd := &cobra.Command{
		Use:     "checkpoint",
		Aliases: []string{"cp"},
		Short:   "Manage pixel checkpoints (ZFS snapshots)",
	}

	createCmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a checkpoint",
		Args:  cobra.ExactArgs(1),
		RunE:  runCheckpointCreate,
	}
	createCmd.Flags().String("label", "", "checkpoint label (default: timestamp)")

	cpCmd.AddCommand(createCmd)
	cpCmd.AddCommand(&cobra.Command{
		Use:   "list <name>",
		Short: "List checkpoints for a pixel",
		Args:  cobra.ExactArgs(1),
		RunE:  runCheckpointList,
	})
	cpCmd.AddCommand(&cobra.Command{
		Use:   "restore <name> <label>",
		Short: "Restore a pixel to a checkpoint",
		Args:  cobra.ExactArgs(2),
		RunE:  runCheckpointRestore,
	})
	cpCmd.AddCommand(&cobra.Command{
		Use:   "delete <name> <label>",
		Short: "Delete a checkpoint",
		Args:  cobra.ExactArgs(2),
		RunE:  runCheckpointDelete,
	})

	rootCmd.AddCommand(cpCmd)
}

func runCheckpointCreate(cmd *cobra.Command, args []string) error {
	name := args[0]
	label, _ := cmd.Flags().GetString("label")

	if label == "" {
		label = "px-" + time.Now().Format("20060102-150405")
	}

	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	if err := sb.CreateSnapshot(cmd.Context(), name, label); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Checkpoint %q created for %s\n", label, name)
	return nil
}

func runCheckpointList(cmd *cobra.Command, args []string) error {
	name := args[0]

	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	snapshots, err := sb.ListSnapshots(cmd.Context(), name)
	if err != nil {
		return err
	}

	if len(snapshots) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "No checkpoints for %s.\n", name)
		return nil
	}

	w := newTabWriter(cmd)
	fmt.Fprintln(w, "LABEL\tSIZE")
	for _, s := range snapshots {
		fmt.Fprintf(w, "%s\t%s\n", s.Label, humanize.IBytes(uint64(s.Size)))
	}
	return w.Flush()
}

func runCheckpointRestore(cmd *cobra.Command, args []string) error {
	name, label := args[0], args[1]

	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	start := time.Now()

	fmt.Fprintf(cmd.ErrOrStderr(), "Restoring %s to %q...\n", name, label)

	if err := sb.RestoreSnapshot(cmd.Context(), name, label); err != nil {
		return err
	}

	elapsed := time.Since(start).Truncate(100 * time.Millisecond)
	fmt.Fprintf(cmd.OutOrStdout(), "Restored %s to %q in %s\n", name, label, elapsed)

	inst, err := sb.Get(cmd.Context(), name)
	if err == nil && len(inst.Addresses) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  SSH: ssh %s@%s\n", cfg.SSH.User, inst.Addresses[0])
	}
	return nil
}

func runCheckpointDelete(cmd *cobra.Command, args []string) error {
	name, label := args[0], args[1]

	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	if err := sb.DeleteSnapshot(cmd.Context(), name, label); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Deleted checkpoint %q from %s\n", label, name)
	return nil
}
