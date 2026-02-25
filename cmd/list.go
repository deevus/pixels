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
	ctx := cmd.Context()

	client, err := connectClient(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	instances, err := client.ListInstances(ctx)
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
		for _, a := range inst.Aliases {
			if (a.Type == "INET" || a.Type == "ipv4") && a.Address != "" {
				ip = a.Address
				break
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", displayName(inst.Name), inst.Status, ip)
	}
	return w.Flush()
}
