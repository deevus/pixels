package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

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

	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	if err := sb.Delete(cmd.Context(), name); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Destroyed %s\n", name)
	return nil
}
