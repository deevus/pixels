package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	truenas "github.com/deevus/truenas-go"
	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/cache"
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
	cmd.Flags().Bool("no-provision", false, "skip all provisioning")
	cmd.Flags().Bool("console", false, "wait for provisioning and open console")
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
	} else {
		// Auto-detect NIC from host's gateway interface.
		nic, err := client.DefaultNIC(ctx)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: NIC auto-detect failed: %v\n", err)
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), "Auto-detected NIC: %s (%s)\n", nic.Parent, nic.NICType)
			opts.NIC = nic
		}
	}

	instance, err := client.CreateInstance(ctx, opts)
	if err != nil {
		return fmt.Errorf("creating instance: %w", err)
	}

	// Provision while the container is running (rootfs only mounted when up).
	noProvision, _ := cmd.Flags().GetBool("no-provision")
	provisionEnabled := cfg.Provision.IsEnabled() && !noProvision

	if provisionEnabled {
		pubKey, _ := readSSHPubKey()
		provOpts := tnc.ProvisionOpts{
			SSHPubKey: pubKey,
			DNS:       cfg.Defaults.DNS,
			Env:       cfg.Env,
			DevTools:  cfg.Provision.DevToolsEnabled(),
		}
		needsProvision := pubKey != "" || len(cfg.Defaults.DNS) > 0 ||
			len(cfg.Env) > 0 || provOpts.DevTools

		if needsProvision {
			fmt.Fprintf(cmd.ErrOrStderr(), "Provisioning...\n")

			if err := client.Provision(ctx, containerName(name), provOpts); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: provisioning failed: %v\n", err)
			} else if pubKey != "" {
				// Restart so systemd picks up rc.local on boot.
				_ = client.Virt.StopInstance(ctx, containerName(name), truenas.StopVirtInstanceOpts{Timeout: 30})
				if err := client.Virt.StartInstance(ctx, containerName(name)); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Warning: restart after provision: %v\n", err)
				}
				// Poll for IP — DHCP assignment takes a few seconds after restart.
				for range 15 {
					instance, err = client.Virt.GetInstance(ctx, containerName(name))
					if err != nil {
						return fmt.Errorf("refreshing instance: %w", err)
					}
					if resolveIP(instance) != "" {
						break
					}
					time.Sleep(time.Second)
				}
			}
		}
	}

	ip := resolveIP(instance)
	if ip != "" && provisionEnabled {
		// Wait for the systemd service to install openssh-server.
		if err := ssh.WaitReady(ctx, ip, 90*time.Second); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: SSH not ready: %v\n", err)
		}
	}

	// Cache IP and status for fast exec/console lookups.
	cache.Put(name, &cache.Entry{IP: ip, Status: instance.Status})

	elapsed := time.Since(start).Truncate(100 * time.Millisecond)
	fmt.Fprintf(cmd.OutOrStdout(), "Created %s in %s\n", containerName(name), elapsed)
	fmt.Fprintf(cmd.OutOrStdout(), "  Hostname: %s\n", containerName(name))
	if ip != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  IP:       %s\n", ip)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  Console:  pixels console %s\n", name)
	openConsole, _ := cmd.Flags().GetBool("console")
	devToolsActive := provisionEnabled && cfg.Provision.DevToolsEnabled()

	if devToolsActive && !openConsole {
		fmt.Fprintf(cmd.OutOrStdout(), "  Dev tools installing in background (sudo journalctl -fu pixels-devtools)\n")
	}

	if openConsole && ip != "" {
		if devToolsActive {
			fmt.Fprintf(cmd.ErrOrStderr(), "Waiting for dev tools to finish installing...\n")
			if err := ssh.WaitProvisioned(ctx, ip, cfg.SSH.User, cfg.SSH.Key, 10*time.Minute); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %v\n", err)
			}
		}
		return ssh.Console(ip, cfg.SSH.User, cfg.SSH.Key)
	}

	return nil
}

// readSSHPubKey reads the SSH public key from the path configured in ssh.key.
// It derives the .pub path from the private key path.
func readSSHPubKey() (string, error) {
	keyPath := cfg.SSH.Key
	if keyPath == "" {
		return "", nil
	}
	pubPath := keyPath + ".pub"
	data, err := os.ReadFile(pubPath)
	if err != nil {
		return "", fmt.Errorf("reading SSH public key %s: %w", pubPath, err)
	}
	return strings.TrimSpace(string(data)), nil
}
