package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/cache"
	"github.com/deevus/pixels/internal/ssh"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "console <name>",
		Short: "Open an interactive SSH session",
		Args:  cobra.ExactArgs(1),
		RunE:  runConsole,
	})
}

func runConsole(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]

	// Try local cache first for fast path (already running).
	var ip string
	if cached := cache.Get(name); cached != nil && cached.IP != "" && cached.Status == "RUNNING" {
		ip = cached.IP
	}

	if ip == "" {
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

		if instance.Status != "RUNNING" {
			fmt.Fprintf(cmd.ErrOrStderr(), "Starting %s...\n", name)
			if err := client.Virt.StartInstance(ctx, containerName(name)); err != nil {
				return fmt.Errorf("starting instance: %w", err)
			}
			instance, err = client.Virt.GetInstance(ctx, containerName(name))
			if err != nil {
				return fmt.Errorf("refreshing instance: %w", err)
			}
		}

		ip = resolveIP(instance)
		if ip == "" {
			return fmt.Errorf("no IP address for %s", name)
		}
		cache.Put(name, &cache.Entry{IP: ip, Status: instance.Status})
	}

	if err := ssh.WaitReady(ctx, ip, 30*time.Second, nil); err != nil {
		return fmt.Errorf("waiting for SSH: %w", err)
	}

	// Verify key auth; if it fails, write this machine's key via TrueNAS.
	if err := ensureSSHAuth(cmd, ctx, ip, name); err != nil {
		return err
	}

	// Console replaces the process — does not return on success.
	return ssh.Console(ip, cfg.SSH.User, cfg.SSH.Key)
}
