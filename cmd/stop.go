package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
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
	name := args[0]

	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	if err := sb.Stop(cmd.Context(), name); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Stopped %s\n", name)
	return nil
}
