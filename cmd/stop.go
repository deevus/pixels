package cmd

import (
	"fmt"

	truenas "github.com/deevus/truenas-go"
	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/cache"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "stop <name>",
		Short: "Stop a running pixel",
		Args:  cobra.ExactArgs(1),
		RunE:  runStop,
	})
}

func runStop(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]

	client, err := connectClient(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.Virt.StopInstance(ctx, containerName(name), truenas.StopVirtInstanceOpts{
		Timeout: 30,
	}); err != nil {
		return fmt.Errorf("stopping %s: %w", name, err)
	}

	cache.Delete(name)
	fmt.Fprintf(cmd.OutOrStdout(), "Stopped %s\n", name)
	return nil
}
