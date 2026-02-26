package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "authorize <name>",
		Short: "Authorize this machine's SSH key on an existing pixel",
		Args:  cobra.ExactArgs(1),
		RunE:  runAuthorize,
	})
}

func runAuthorize(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]

	pubKey, err := readSSHPubKey()
	if err != nil {
		return err
	}
	if pubKey == "" {
		return fmt.Errorf("no SSH key configured — set ssh.key in config or PIXELS_SSH_KEY")
	}

	client, err := connectClient(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	// Verify the container exists and is running (rootfs must be mounted).
	instance, err := client.Virt.GetInstance(ctx, containerName(name))
	if err != nil {
		return fmt.Errorf("looking up %s: %w", name, err)
	}
	if instance.Status != "RUNNING" {
		return fmt.Errorf("%s is %s — must be running to authorize", name, instance.Status)
	}

	if err := client.AuthorizeKey(ctx, containerName(name), pubKey); err != nil {
		return fmt.Errorf("authorizing key on %s: %w", name, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Authorized SSH key on %s\n", name)
	return nil
}
