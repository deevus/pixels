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

	pubKey, _ := readSSHPubKey()

	// Fast path: cache hit with matching SSH key — no TrueNAS connection needed.
	var ip string
	cached := cache.Get(name)
	if cached != nil && cached.IP != "" && cached.Status == "RUNNING" && cached.SSHPubKey == pubKey {
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

		// Write SSH key if configured (ensures this machine is authorized).
		if pubKey != "" {
			if err := client.WriteAuthorizedKey(ctx, containerName(name), pubKey); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: writing SSH key: %v\n", err)
			}
		}

		cache.Put(name, &cache.Entry{IP: ip, Status: instance.Status, SSHPubKey: pubKey})
	}

	if err := ssh.WaitReady(ctx, ip, 30*time.Second); err != nil {
		return fmt.Errorf("waiting for SSH: %w", err)
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
