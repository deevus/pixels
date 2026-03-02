package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List all pixels",
		Args:  cobra.NoArgs,
		RunE:  runList,
	})
}

func runList(cmd *cobra.Command, _ []string) error {
	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	instances, err := sb.List(cmd.Context())
	if err != nil {
		return err
	}

	if len(instances) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No pixels found.")
		return nil
	}

	w := newTabWriter(cmd)
	fmt.Fprintln(w, "NAME\tSTATUS\tIP")
	for _, inst := range instances {
		ip := "—"
		if len(inst.Addresses) > 0 {
			ip = inst.Addresses[0]
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", inst.Name, inst.Status, ip)
	}
	return w.Flush()
}
