package cmd

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/cache"
	"github.com/deevus/pixels/internal/provision"
	"github.com/deevus/pixels/internal/ssh"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "status <name>",
		Short: "Show provisioning step status",
		Args:  cobra.ExactArgs(1),
		RunE:  runStatus,
	})
}

func runStatus(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]

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
			return fmt.Errorf("pixel %q is not running (status: %s)", name, instance.Status)
		}

		ip = resolveIP(instance)
		if ip == "" {
			return fmt.Errorf("no IP address for %s", name)
		}
	}

	if err := ssh.WaitReady(ctx, ip, 10*time.Second, nil); err != nil {
		return fmt.Errorf("waiting for SSH: %w", err)
	}

	runner := provision.NewRunner(ip, "root", cfg.SSH.Key)
	raw, err := runner.List(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "No such file") {
			fmt.Fprintln(cmd.OutOrStdout(), "No provisioning steps found (zmx not installed)")
			return nil
		}
		return err
	}

	sessions := provision.ParseSessions(raw)

	// Filter to px-* sessions (our provisioning steps).
	var steps []provision.Session
	for _, s := range sessions {
		if strings.HasPrefix(s.Name, "px-") {
			steps = append(steps, s)
		}
	}

	if len(steps) == 0 {
		if runner.IsProvisioned(ctx) {
			fmt.Fprintln(cmd.OutOrStdout(), "Provisioning complete")
		} else if runner.HasProvisionScript(ctx) {
			fmt.Fprintln(cmd.OutOrStdout(), "Provisioning in progress...")
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "No provisioning steps found")
		}
		return nil
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "STEP\tSTATUS\tEXIT")
	for _, s := range steps {
		status := "running"
		exit := "-"
		if s.EndedAt != "" {
			status = "done"
			exit = s.ExitCode
			if exit != "0" {
				status = "failed"
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", s.Name, status, exit)
	}
	w.Flush()

	return nil
}
