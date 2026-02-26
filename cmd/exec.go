package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/cache"
	"github.com/deevus/pixels/internal/ssh"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "exec <name> -- <command...>",
		Short: "Run a command in a pixel via SSH",
		Args:  cobra.MinimumNArgs(2),
		RunE:  runExec,
	})
}

func runExec(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	command := args[1:]

	// Try local cache first to avoid the WebSocket round-trip.
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
			return fmt.Errorf("pixel %q is %s — start it first", name, instance.Status)
		}

		ip = resolveIP(instance)
		if ip == "" {
			return fmt.Errorf("no IP address for %s", name)
		}
		cache.Put(name, &cache.Entry{IP: ip, Status: instance.Status})
	}

	if err := ssh.WaitReady(ctx, ip, 30*time.Second); err != nil {
		return fmt.Errorf("waiting for SSH: %w", err)
	}

	// Verify key auth; if it fails, write this machine's key via TrueNAS.
	if err := ensureSSHAuth(cmd, ctx, ip, name); err != nil {
		return err
	}

	exitCode, err := ssh.Exec(ctx, ip, cfg.SSH.User, cfg.SSH.Key, command)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
	return nil
}
