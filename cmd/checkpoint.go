package cmd

import (
	"context"
	"fmt"
	"time"

	truenas "github.com/deevus/truenas-go"
	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/cache"
	"github.com/deevus/pixels/internal/ssh"
	tnc "github.com/deevus/pixels/internal/truenas"
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

// resolveDatasetPath returns the ZFS dataset path for a container.
// Priority: config override > auto-detect from virt.global.config.
func resolveDatasetPath(ctx context.Context, client *tnc.Client, name string) (string, error) {
	if cfg.Checkpoint.DatasetPrefix != "" {
		return cfg.Checkpoint.DatasetPrefix + "/" + containerName(name), nil
	}
	return client.ContainerDataset(ctx, containerName(name))
}

func runCheckpointCreate(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	label, _ := cmd.Flags().GetString("label")

	if label == "" {
		label = "px-" + time.Now().Format("20060102-150405")
	}

	client, err := connectClient(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	ds, err := resolveDatasetPath(ctx, client, name)
	if err != nil {
		return err
	}

	_, err = client.Snapshot.Create(ctx, truenas.CreateSnapshotOpts{
		Dataset: ds,
		Name:    label,
	})
	if err != nil {
		return fmt.Errorf("creating checkpoint: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Checkpoint %q created for %s\n", label, name)
	return nil
}

func runCheckpointList(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]

	client, err := connectClient(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	ds, err := resolveDatasetPath(ctx, client, name)
	if err != nil {
		return err
	}

	snapshots, err := client.ListSnapshots(ctx, ds)
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
		fmt.Fprintf(w, "%s\t%s\n", s.SnapshotName, formatBytes(s.Referenced))
	}
	return w.Flush()
}

func runCheckpointRestore(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name, label := args[0], args[1]

	client, err := connectClient(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	ds, err := resolveDatasetPath(ctx, client, name)
	if err != nil {
		return err
	}
	sid := ds + "@" + label

	start := time.Now()

	fmt.Fprintf(cmd.ErrOrStderr(), "Stopping %s...\n", name)
	if err := client.Virt.StopInstance(ctx, containerName(name), truenas.StopVirtInstanceOpts{
		Timeout: 30,
	}); err != nil {
		return fmt.Errorf("stopping %s: %w", name, err)
	}

	if err := client.SnapshotRollback(ctx, sid); err != nil {
		return err
	}

	if err := client.Virt.StartInstance(ctx, containerName(name)); err != nil {
		return fmt.Errorf("starting %s: %w", name, err)
	}

	instance, err := client.Virt.GetInstance(ctx, containerName(name))
	if err != nil {
		return fmt.Errorf("refreshing %s: %w", name, err)
	}

	ip := resolveIP(instance)
	pubKey, _ := readSSHPubKey()
	cache.Put(name, &cache.Entry{IP: ip, Status: instance.Status, SSHPubKey: pubKey})
	if ip != "" {
		if err := ssh.WaitReady(ctx, ip, 30*time.Second); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: SSH not ready: %v\n", err)
		}
	}

	elapsed := time.Since(start).Truncate(100 * time.Millisecond)
	fmt.Fprintf(cmd.OutOrStdout(), "Restored %s to %q in %s\n", name, label, elapsed)
	if ip != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  SSH: ssh %s@%s\n", cfg.SSH.User, ip)
	}
	return nil
}

func runCheckpointDelete(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name, label := args[0], args[1]

	client, err := connectClient(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	ds, err := resolveDatasetPath(ctx, client, name)
	if err != nil {
		return err
	}
	sid := ds + "@" + label

	if err := client.Snapshot.Delete(ctx, sid); err != nil {
		return fmt.Errorf("deleting checkpoint: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Deleted checkpoint %q from %s\n", label, name)
	return nil
}
