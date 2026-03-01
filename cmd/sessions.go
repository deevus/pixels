package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/cache"
	"github.com/deevus/pixels/internal/provision"
	"github.com/deevus/pixels/internal/ssh"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "sessions <name>",
		Short: "List zmx sessions in a container",
		Args:  cobra.ExactArgs(1),
		RunE:  runSessions,
	})
}

func runSessions(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]

	// Try local cache first for fast path.
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

	if err := ssh.WaitReady(ctx, ip, 30*time.Second, nil); err != nil {
		return fmt.Errorf("waiting for SSH: %w", err)
	}

	cc := ssh.ConnConfig{Host: ip, User: cfg.SSH.User, KeyPath: cfg.SSH.Key}
	out, err := ssh.OutputQuiet(ctx, cc, []string{"unset XDG_RUNTIME_DIR && zmx list"})
	if err != nil {
		return fmt.Errorf("zmx not available on %s", name)
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		fmt.Fprintln(cmd.OutOrStdout(), "No sessions")
		return nil
	}

	sessions := provision.ParseSessions(raw)
	if len(sessions) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No sessions")
		return nil
	}

	tw := newTabWriter(cmd)
	fmt.Fprintln(tw, "SESSION\tSTATUS")
	for _, s := range sessions {
		status := "running"
		if s.EndedAt != "" {
			status = "exited"
		}
		fmt.Fprintf(tw, "%s\t%s\n", s.Name, status)
	}
	return tw.Flush()
}
