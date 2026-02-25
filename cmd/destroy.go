package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	truenas "github.com/deevus/truenas-go"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:   "destroy <name>",
		Short: "Permanently destroy a pixel and all its checkpoints",
		Args:  cobra.ExactArgs(1),
		RunE:  runDestroy,
	}
	cmd.Flags().Bool("force", false, "skip confirmation prompt")
	rootCmd.AddCommand(cmd)
}

func runDestroy(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	force, _ := cmd.Flags().GetBool("force")

	if !force {
		fmt.Fprintf(cmd.OutOrStdout(), "Destroy pixel %q and all its checkpoints? [y/N] ", name)
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			return nil
		}
		answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(cmd.OutOrStdout(), "Aborted.")
			return nil
		}
	}

	client, err := connectClient(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	instance, err := client.Virt.GetInstance(ctx, containerName(name))
	if err != nil {
		return fmt.Errorf("looking up %s: %w", name, err)
	}
	if instance == nil {
		return fmt.Errorf("pixel %q not found", name)
	}

	if instance.Status == "RUNNING" {
		fmt.Fprintf(cmd.ErrOrStderr(), "Stopping %s...\n", name)
		if err := client.Virt.StopInstance(ctx, containerName(name), truenas.StopVirtInstanceOpts{
			Timeout: 30,
		}); err != nil {
			return fmt.Errorf("stopping %s: %w", name, err)
		}
	}

	if err := client.Virt.DeleteInstance(ctx, containerName(name)); err != nil {
		return fmt.Errorf("deleting %s: %w", name, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Destroyed %s\n", name)
	return nil
}
