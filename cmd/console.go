package cmd

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/briandowns/spinner"
	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/cache"
	"github.com/deevus/pixels/internal/provision"
	"github.com/deevus/pixels/internal/ssh"
)

var validSessionName = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func init() {
	cmd := &cobra.Command{
		Use:   "console <name>",
		Short: "Open a persistent SSH session (zmx)",
		Args:  cobra.ExactArgs(1),
		RunE:  runConsole,
	}
	cmd.Flags().StringP("session", "s", "console", "zmx session name")
	cmd.Flags().Bool("no-persist", false, "skip zmx, use plain SSH")
	rootCmd.AddCommand(cmd)
}

func runConsole(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]

	session, _ := cmd.Flags().GetString("session")
	noPersist, _ := cmd.Flags().GetBool("no-persist")

	if !noPersist && !validSessionName.MatchString(session) {
		return fmt.Errorf("invalid session name %q: must match [a-zA-Z0-9._-]", session)
	}

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

	// Wait for provisioning to finish before opening the console.
	runner := provision.NewRunner(ip, "root", cfg.SSH.Key)
	var spin *spinner.Spinner
	if !verbose {
		spin = spinner.New(spinner.CharSets[14], 100*time.Millisecond, spinner.WithWriter(cmd.ErrOrStderr()))
	}
	runner.WaitProvisioned(ctx, func(status string) {
		if spin != nil {
			spin.Suffix = "  " + status
			if !spin.Active() {
				spin.Start()
			}
		} else {
			logv(cmd, "Provision: %s", status)
		}
	})
	if spin != nil && spin.Active() {
		spin.Stop()
	}

	cc := ssh.ConnConfig{Host: ip, User: cfg.SSH.User, KeyPath: cfg.SSH.Key, Env: cfg.EnvForward}

	// Determine remote command for zmx session persistence.
	var remoteCmd string
	if !noPersist {
		remoteCmd = zmxRemoteCmd(ctx, cc, session)
	}

	// Console replaces the process — does not return on success.
	return ssh.Console(cc, remoteCmd)
}

// zmxRemoteCmd checks if zmx is available in the container and returns the
// attach command string. Returns empty string if zmx is not installed.
func zmxRemoteCmd(ctx context.Context, cc ssh.ConnConfig, session string) string {
	// Check without env forwarding to avoid polluting the zmx check.
	checkCC := ssh.ConnConfig{Host: cc.Host, User: cc.User, KeyPath: cc.KeyPath}
	code, err := ssh.ExecQuiet(ctx, checkCC, []string{"command -v zmx >/dev/null 2>&1"})
	if err == nil && code == 0 {
		return "unset XDG_RUNTIME_DIR && zmx attach " + session + " bash -l"
	}
	return ""
}
