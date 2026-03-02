package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "start <name>",
		Short: "Start a stopped pixel",
		Args:  cobra.ExactArgs(1),
		RunE:  runStart,
	})
}

func runStart(cmd *cobra.Command, args []string) error {
	name := args[0]

	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	if err := sb.Start(cmd.Context(), name); err != nil {
		return err
	}

	inst, err := sb.Get(cmd.Context(), name)
	if err != nil {
		// Start succeeded but Get failed — still report success.
		fmt.Fprintf(cmd.OutOrStdout(), "Started %s\n", name)
		return nil
	}

	if len(inst.Addresses) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Started %s\n", name)
		fmt.Fprintf(cmd.OutOrStdout(), "  IP:  %s\n", inst.Addresses[0])
		fmt.Fprintf(cmd.OutOrStdout(), "  SSH: ssh %s@%s\n", cfg.SSH.User, inst.Addresses[0])
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Started %s (no IP assigned)\n", name)
	}
	return nil
}
