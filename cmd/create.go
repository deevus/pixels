package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/briandowns/spinner"
	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/provision"
	"github.com/deevus/pixels/sandbox"
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

	// Spinner for non-verbose mode — shows current phase on stderr.
	var spin *spinner.Spinner
	if !verbose {
		spin = spinner.New(spinner.CharSets[14], 100*time.Millisecond, spinner.WithWriter(cmd.ErrOrStderr()))
	}
	setStatus := func(msg string) {
		if spin != nil {
			spin.Suffix = "  " + msg
			if !spin.Active() {
				spin.Start()
			}
		}
	}
	stopSpinner := func() {
		if spin != nil && spin.Active() {
			spin.Stop()
		}
	}
	defer stopSpinner()

	// Build sandbox config with any overrides.
	m := sandboxConfig()
	noProvision, _ := cmd.Flags().GetBool("no-provision")
	if noProvision {
		m["provision"] = "false"
	}
	if egressMode != "" {
		m["egress"] = egressMode
	}

	sb, err := sandbox.Open(cfg.Backend, m)
	if err != nil {
		return err
	}
	defer sb.Close()

	start := time.Now()

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

	if from != "" {
		// Clone-from-checkpoint flow.
		if tempSnapshot {
			fromLabel = "px-clone-" + time.Now().Format("20060102-150405")
			if err := sb.CreateSnapshot(ctx, fromSource, fromLabel); err != nil {
				return fmt.Errorf("snapshotting %s: %w", fromSource, err)
			}
			defer func() {
				_ = sb.DeleteSnapshot(ctx, fromSource, fromLabel)
			}()
		}

		if tempSnapshot {
			setStatus(fmt.Sprintf("Cloning from %s...", fromSource))
		} else {
			setStatus(fmt.Sprintf("Cloning from %s checkpoint %q...", fromSource, fromLabel))
		}

		// Create a bare container (no provisioning/SSH wait) — we'll replace its rootfs.
		logv(cmd, "Creating bare container %s...", name)
		_, err := sb.Create(ctx, sandbox.CreateOpts{
			Name:   name,
			Image:  image,
			CPU:    cpu,
			Memory: memory * 1024 * 1024,
			Bare:   true,
		})
		if err != nil {
			return fmt.Errorf("creating instance: %w", err)
		}

		logv(cmd, "Stopping %s for rootfs replacement...", name)
		if err := sb.Stop(ctx, name); err != nil {
			return fmt.Errorf("stopping %s for clone: %w", name, err)
		}

		logv(cmd, "Cloning snapshot %s:%s...", fromSource, fromLabel)
		if err := sb.CloneFrom(ctx, fromSource, fromLabel, name); err != nil {
			_ = sb.Delete(ctx, name)
			return fmt.Errorf("cloning checkpoint: %w", err)
		}

		if err := sb.Start(ctx, name); err != nil {
			return fmt.Errorf("starting %s: %w", name, err)
		}

		setStatus("Waiting for SSH...")
		_ = sb.Ready(ctx, name, 30*time.Second)
	} else {
		// Normal create flow — sandbox handles provisioning, IP poll, SSH wait.
		setStatus("Creating...")
		logv(cmd, "Creating container %s...", name)
		_, err = sb.Create(ctx, sandbox.CreateOpts{
			Name:   name,
			Image:  image,
			CPU:    cpu,
			Memory: memory * 1024 * 1024,
		})
		if err != nil {
			return fmt.Errorf("creating instance: %w", err)
		}
	}

	// Fetch final instance state for display.
	inst, err := sb.Get(ctx, name)
	if err != nil {
		return fmt.Errorf("refreshing %s: %w", name, err)
	}

	var ip string
	if len(inst.Addresses) > 0 {
		ip = inst.Addresses[0]
	}

	// Compute provisioning steps for status hint.
	steps := provision.Steps(egressMode, cfg.Provision.DevToolsEnabled())

	stopSpinner()
	elapsed := time.Since(start).Truncate(100 * time.Millisecond)
	fmt.Fprintf(cmd.OutOrStdout(), "Created %s in %s\n", containerName(name), elapsed)
	fmt.Fprintf(cmd.OutOrStdout(), "  Hostname: %s\n", containerName(name))
	if ip != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  IP:       %s\n", ip)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  Console:  pixels console %s\n", name)
	openConsole, _ := cmd.Flags().GetBool("console")

	if len(steps) > 0 && !openConsole {
		fmt.Fprintf(cmd.OutOrStdout(), "  Status:   pixels status %s\n", name)
	}

	if openConsole {
		runner := provision.NewRunnerWith(&sandboxExecutor{sb: sb, name: name})
		runner.WaitProvisioned(ctx, func(status string) {
			setStatus(status)
			logv(cmd, "Provision: %s", status)
		})
		stopSpinner()

		var envSlice []string
		for k, v := range cfg.EnvForward {
			envSlice = append(envSlice, k+"="+v)
		}

		remoteCmd := zmxRemoteCmdViaSandbox(ctx, sb, name, "console")
		return sb.Console(ctx, name, sandbox.ConsoleOpts{
			Env:       envSlice,
			RemoteCmd: remoteCmd,
		})
	}

	return nil
}
