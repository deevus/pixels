package cmd

import (
	"context"
	"fmt"
	"io"
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
	cmd.Flags().String("from", "", "create from checkpoint (container:label)")
	cmd.Flags().String("egress", "", "egress policy: unrestricted, agent, allowlist (default from config)")
	rootCmd.AddCommand(cmd)
}

func runCreate(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]

	image, _ := cmd.Flags().GetString("image")
	cpu, _ := cmd.Flags().GetString("cpu")
	memory, _ := cmd.Flags().GetInt64("memory")
	from, _ := cmd.Flags().GetString("from")

	if image == "" {
		image = cfg.Defaults.Image
	}
	if cpu == "" {
		cpu = cfg.Defaults.CPU
	}
	if memory == 0 {
		memory = cfg.Defaults.Memory
	}

	egressMode, _ := cmd.Flags().GetString("egress")
	if egressMode == "" {
		egressMode = cfg.Network.Egress
	}
	switch egressMode {
	case "unrestricted", "agent", "allowlist", "":
		// valid
	default:
		return fmt.Errorf("invalid --egress %q: must be unrestricted, agent, or allowlist", egressMode)
	}

	logv(cmd, "Config: image=%s cpu=%s memory=%dMiB egress=%s", image, cpu, memory, egressMode)

	// Parse --from flag: "container" or "container:label"
	var fromSource, fromLabel string
	var tempSnapshot bool
	if from != "" {
		if parts := strings.SplitN(from, ":", 2); len(parts) == 2 {
			if parts[0] == "" || parts[1] == "" {
				return fmt.Errorf("--from must be container or container:label (e.g. --from base or --from base:ready)")
			}
			fromSource, fromLabel = parts[0], parts[1]
		} else {
			fromSource = from
			tempSnapshot = true
		}
	}

	logv(cmd, "Connecting to %s...", cfg.TrueNAS.Host)
	client, err := connectClient(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	start := time.Now()

	// When cloning from a checkpoint, verify/create it before creating
	// the container so we fail fast without leaving anything to clean up.
	skipProvision := fromSource != ""
	var snapshotID string
	if skipProvision {
		srcDS, err := resolveDatasetPath(ctx, client, fromSource)
		if err != nil {
			return fmt.Errorf("resolving source dataset: %w", err)
		}

		if tempSnapshot {
			// Auto-snapshot the source container's current state.
			fromLabel = "px-clone-" + time.Now().Format("20060102-150405")
			if _, err := client.Snapshot.Create(ctx, truenas.CreateSnapshotOpts{
				Dataset: srcDS,
				Name:    fromLabel,
			}); err != nil {
				return fmt.Errorf("snapshotting %s: %w", fromSource, err)
			}
			defer func() {
				_ = client.Snapshot.Delete(ctx, srcDS+"@"+fromLabel)
			}()
		}

		snapshotID = srcDS + "@" + fromLabel

		if !tempSnapshot {
			snap, err := client.Snapshot.Get(ctx, snapshotID)
			if err != nil {
				return fmt.Errorf("looking up checkpoint: %w", err)
			}
			if snap == nil {
				return fmt.Errorf("checkpoint %q not found for %s", fromLabel, fromSource)
			}
		}
	}

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

	logv(cmd, "Creating container %s (image=%s)...", containerName(name), image)
	instance, err := client.CreateInstance(ctx, opts)
	if err != nil {
		return fmt.Errorf("creating instance: %w", err)
	}
	logv(cmd, "Container created (status=%s)", instance.Status)

	// Clone-from-checkpoint: stop the new container, destroy its ZFS dataset,
	// and clone the checkpoint snapshot in its place via a temporary cron job
	// (pool.dataset.* APIs can't see .ix-virt managed datasets).
	if skipProvision {
		if tempSnapshot {
			fmt.Fprintf(cmd.ErrOrStderr(), "Cloning from %s...\n", fromSource)
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), "Cloning from %s checkpoint %q...\n", fromSource, fromLabel)
		}

		logv(cmd, "Stopping %s for rootfs replacement...", containerName(name))
		if err := client.Virt.StopInstance(ctx, containerName(name), truenas.StopVirtInstanceOpts{Timeout: 30}); err != nil {
			return fmt.Errorf("stopping %s for clone: %w", name, err)
		}

		logv(cmd, "Cloning ZFS snapshot %s...", snapshotID)
		if err := client.ReplaceContainerRootfs(ctx, containerName(name), snapshotID); err != nil {
			_ = client.Virt.DeleteInstance(ctx, containerName(name))
			return fmt.Errorf("cloning checkpoint: %w", err)
		}

		if err := client.Virt.StartInstance(ctx, containerName(name)); err != nil {
			return fmt.Errorf("starting %s: %w", name, err)
		}

		instance, err = client.Virt.GetInstance(ctx, containerName(name))
		if err != nil {
			return fmt.Errorf("refreshing instance: %w", err)
		}
	}

	// Provision while the container is running (rootfs only mounted when up).
	noProvision, _ := cmd.Flags().GetBool("no-provision")
	provisionEnabled := cfg.Provision.IsEnabled() && !noProvision && !skipProvision

	if provisionEnabled {
		pubKey, _ := readSSHPubKey()
		provOpts := tnc.ProvisionOpts{
			SSHPubKey:   pubKey,
			DNS:         cfg.Defaults.DNS,
			Env:         cfg.Env,
			DevTools:    cfg.Provision.DevToolsEnabled(),
			Egress:      egressMode,
			EgressAllow: cfg.Network.Allow,
		}
		if verbose {
			provOpts.Log = cmd.ErrOrStderr()
		}
		needsProvision := pubKey != "" || len(cfg.Defaults.DNS) > 0 ||
			len(cfg.Env) > 0 || provOpts.DevTools

		if needsProvision {
			fmt.Fprintf(cmd.ErrOrStderr(), "Provisioning...\n")
			logv(cmd, "SSH key: %v, DNS: %d, Env: %d, DevTools: %v, Egress: %s",
				pubKey != "", len(cfg.Defaults.DNS), len(cfg.Env), provOpts.DevTools, egressMode)

			if err := client.Provision(ctx, containerName(name), provOpts); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: provisioning failed: %v\n", err)
			} else if pubKey != "" {
				// Restart so systemd picks up rc.local on boot.
				logv(cmd, "Restarting container for rc.local execution...")
				_ = client.Virt.StopInstance(ctx, containerName(name), truenas.StopVirtInstanceOpts{Timeout: 30})
				if err := client.Virt.StartInstance(ctx, containerName(name)); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Warning: restart after provision: %v\n", err)
				}
				// Poll for IP — DHCP assignment takes a few seconds after restart.
				logv(cmd, "Waiting for IP assignment...")
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
	if ip != "" {
		// SSH wait: 90s for fresh images (openssh-server install), 30s for clones.
		timeout := 90 * time.Second
		if skipProvision {
			timeout = 30 * time.Second
		}
		if provisionEnabled || skipProvision {
			var sshLog io.Writer
			if verbose {
				sshLog = cmd.ErrOrStderr()
			}
			if err := ssh.WaitReady(ctx, ip, timeout, sshLog); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: SSH not ready: %v\n", err)
			}
		}
	}

	// Cache IP and status for fast exec/console lookups.
	cache.Put(name, &cache.Entry{IP: ip, Status: instance.Status})
	logv(cmd, "Cached IP=%s status=%s for %s", ip, instance.Status, name)

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

			// Stream journal output so the user can see progress.
			journalCtx, journalCancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() {
				defer close(done)
				ssh.Exec(journalCtx, ip, "root", cfg.SSH.Key,
					[]string{"journalctl", "-fu", "pixels-devtools", "--no-pager", "-o", "cat"})
			}()

			if err := ssh.WaitProvisioned(ctx, ip, cfg.SSH.User, cfg.SSH.Key, 10*time.Minute); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %v\n", err)
			}
			journalCancel()
			<-done
		}
		return ssh.Console(ip, cfg.SSH.User, cfg.SSH.Key)
	}

	return nil
}
