package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/ssh"
	tnc "github.com/deevus/pixels/internal/truenas"
)

func init() {
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new pixel",
		Args:  cobra.ExactArgs(1),
		RunE:  runCreate,
	}
	cmd.Flags().String("image", "", "container image (default from config)")
	cmd.Flags().String("cpu", "", "CPU cores (default from config)")
	cmd.Flags().Int64("memory", 0, "memory in MiB (default from config)")
	rootCmd.AddCommand(cmd)
}

func runCreate(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]

	image, _ := cmd.Flags().GetString("image")
	cpu, _ := cmd.Flags().GetString("cpu")
	memory, _ := cmd.Flags().GetInt64("memory")

	if image == "" {
		image = cfg.Defaults.Image
	}
	if cpu == "" {
		cpu = cfg.Defaults.CPU
	}
	if memory == 0 {
		memory = cfg.Defaults.Memory
	}

	client, err := connectClient(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	start := time.Now()

	opts := tnc.CreateInstanceOpts{
		Name:      containerName(name),
		Image:     image,
		CPU:       cpu,
		Memory:    memory * 1024 * 1024, // MiB → bytes
		Autostart: true,
	}

	if cfg.Defaults.NICType != "" {
		opts.NIC = &tnc.NICOpts{
			NICType: strings.ToUpper(cfg.Defaults.NICType),
			Parent:  cfg.Defaults.Parent,
		}
	}

	instance, err := client.CreateInstance(ctx, opts)
	if err != nil {
		return fmt.Errorf("creating instance: %w", err)
	}

	ip := resolveIP(instance)
	if ip != "" {
		if err := ssh.WaitReady(ctx, ip, 30*time.Second); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: SSH not ready: %v\n", err)
		}
	}

	elapsed := time.Since(start).Truncate(100 * time.Millisecond)
	fmt.Fprintf(cmd.OutOrStdout(), "Created %s in %s\n", containerName(name), elapsed)
	if ip != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  IP:  %s\n", ip)
		fmt.Fprintf(cmd.OutOrStdout(), "  SSH: ssh %s@%s\n", cfg.SSH.User, ip)
	}
	return nil
}
