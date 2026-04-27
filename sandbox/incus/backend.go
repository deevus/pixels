package incus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	incusclient "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"

	"github.com/deevus/pixels/internal/egress"
	"github.com/deevus/pixels/internal/provision"
	"github.com/deevus/pixels/internal/retry"
	"github.com/deevus/pixels/internal/scripts"
	"github.com/deevus/pixels/sandbox"
)

const containerPrefix = "px-"

func prefixed(name string) string  { return containerPrefix + name }
func unprefixed(name string) string { return strings.TrimPrefix(name, containerPrefix) }

// Create creates a new container instance with the full provisioning flow.
// When opts.Bare is true, only the instance is created (no provisioning).
func (i *Incus) Create(ctx context.Context, opts sandbox.CreateOpts) (*sandbox.Instance, error) {
	name := opts.Name
	full := prefixed(name)

	image := opts.Image
	if image == "" {
		image = i.cfg.image
	}
	cpu := opts.CPU
	if cpu == "" {
		cpu = i.cfg.cpu
	}
	memory := opts.Memory
	if memory == 0 {
		memory = i.cfg.memory * 1024 * 1024 // MiB → bytes
	}

	config := map[string]string{
		"limits.cpu":    cpu,
		"limits.memory": fmt.Sprintf("%d", memory),
	}

	devices := map[string]map[string]string{}
	if i.cfg.network != "" {
		devices["eth0"] = map[string]string{
			"type":    "nic",
			"name":    "eth0",
			"network": i.cfg.network,
		}
	} else if i.cfg.parent != "" {
		nicType := "macvlan"
		if i.cfg.nicType != "" {
			nicType = strings.ToLower(i.cfg.nicType)
		}
		devices["eth0"] = map[string]string{
			"type":    "nic",
			"name":    "eth0",
			"nictype": nicType,
			"parent":  i.cfg.parent,
		}
	}

	source := api.InstanceSource{
		Type:  "image",
		Alias: image,
	}
	if i.cfg.imageServer != "" {
		source.Server   = i.cfg.imageServer
		source.Protocol = "simplestreams"
	}

	req := api.InstancesPost{
		Name:   full,
		Type:   api.InstanceTypeContainer,
		Source: source,
		InstancePut: api.InstancePut{
			Config:  config,
			Devices: devices,
		},
	}

	op, err := i.server.CreateInstance(req)
	if err != nil {
		return nil, fmt.Errorf("creating instance: %w", err)
	}
	if err := op.WaitContext(ctx); err != nil {
		return nil, fmt.Errorf("waiting for instance creation: %w", err)
	}

	// Start the instance.
	startOp, err := i.server.UpdateInstanceState(full, api.InstanceStatePut{
		Action:  "start",
		Timeout: -1,
	}, "")
	if err != nil {
		return nil, fmt.Errorf("starting instance: %w", err)
	}
	if err := startOp.WaitContext(ctx); err != nil {
		return nil, fmt.Errorf("waiting for instance start: %w", err)
	}

	if opts.Bare {
		inst, _, err := i.server.GetInstance(full)
		if err != nil {
			return nil, fmt.Errorf("getting instance: %w", err)
		}
		return &sandbox.Instance{
			Name:   name,
			Status: normalizeStatus(inst.Status),
		}, nil
	}

	// Wait for the Incus agent to be ready.
	if err := i.waitAgentReady(ctx, full, 60*time.Second); err != nil {
		return nil, fmt.Errorf("waiting for agent: %w", err)
	}

	// Provision if enabled.
	if i.cfg.provision {
		if err := i.provisionInstance(ctx, full); err != nil {
			// Non-fatal: log and continue.
			_ = err
		}
	}

	// Poll for IP assignment.
	var addrs []string
	if err := retry.Poll(ctx, time.Second, 30*time.Second, func(ctx context.Context) (bool, error) {
		state, _, err := i.server.GetInstanceState(full)
		if err != nil {
			return false, nil
		}
		addrs = extractIPv4(state)
		return len(addrs) > 0, nil
	}); err != nil && !errors.Is(err, retry.ErrTimeout) {
		return nil, err
	}

	inst, _, err := i.server.GetInstance(full)
	if err != nil {
		return nil, fmt.Errorf("getting instance: %w", err)
	}

	return &sandbox.Instance{
		Name:      name,
		Status:    normalizeStatus(inst.Status),
		Addresses: addrs,
	}, nil
}

// provisionInstance pushes files and runs bootstrap inside the container.
func (i *Incus) provisionInstance(ctx context.Context, full string) error {
	pubKey := readSSHPubKey(i.cfg.sshKey)
	steps := provision.Steps(i.cfg.egress, i.cfg.devtools)

	// Ensure directories exist.
	i.mkdir(full, "/root/.ssh", 0o700)
	i.mkdir(full, "/home/pixel", 0o755)
	i.mkdir(full, "/home/pixel/.ssh", 0o700)
	i.mkdir(full, "/etc/systemd/resolved.conf.d", 0o755)

	i.mkdir(full, "/etc/profile.d", 0o755)
	i.mkdir(full, "/etc/sudoers.d", 0o755)

	// DNS config.
	if len(i.cfg.dns) > 0 {
		var conf strings.Builder
		conf.WriteString("[Resolve]\nDNS=")
		conf.WriteString(strings.Join(i.cfg.dns, " "))
		conf.WriteString("\n")
		if err := i.pushFile(full, "/etc/systemd/resolved.conf.d/pixels-dns.conf", []byte(conf.String()), 0o644); err != nil {
			return fmt.Errorf("writing DNS config: %w", err)
		}
	}

	// Shell profile.
	if err := i.pushFile(full, "/etc/profile.d/pixels.sh", []byte(scripts.PixelsProfile), 0o644); err != nil {
		return fmt.Errorf("writing profile: %w", err)
	}

	// Environment variables.
	if len(i.cfg.env) > 0 {
		var envBuf strings.Builder
		for k, v := range i.cfg.env {
			fmt.Fprintf(&envBuf, "%s=%q\n", k, v)
		}
		if err := i.pushFile(full, "/etc/environment", []byte(envBuf.String()), 0o644); err != nil {
			return fmt.Errorf("writing /etc/environment: %w", err)
		}
	}

	// SSH authorized keys.
	if pubKey != "" {
		keyData := []byte(pubKey + "\n")
		if err := i.pushFile(full, "/root/.ssh/authorized_keys", keyData, 0o600); err != nil {
			return fmt.Errorf("writing root authorized_keys: %w", err)
		}
		if err := i.pushFileOwned(full, "/home/pixel/.ssh/authorized_keys", keyData, 0o600, 1000, 1000); err != nil {
			return fmt.Errorf("writing pixel authorized_keys: %w", err)
		}
	}

	// Dev tools script.
	if i.cfg.devtools {
		if err := i.pushFile(full, "/usr/local/bin/pixels-setup-devtools.sh", []byte(scripts.SetupDevtools), 0o755); err != nil {
			return fmt.Errorf("writing devtools script: %w", err)
		}
	}

	// Egress files.
	isRestricted := i.cfg.egress == "agent" || i.cfg.egress == "allowlist"
	if isRestricted {
		if err := i.pushEgressFiles(ctx, full, i.cfg.egress, i.cfg.allow); err != nil {
			return err
		}
	}

	// Provision script.
	if len(steps) > 0 {
		script := provision.Script(steps)
		if err := i.pushFile(full, "/usr/local/bin/pixels-provision.sh", []byte(script), 0o755); err != nil {
			return fmt.Errorf("writing provision script: %w", err)
		}
	}

	// rc.local — runs SSH bootstrap on first boot.
	if pubKey != "" || i.cfg.devtools {
		if err := i.pushFile(full, "/etc/rc.local", []byte(scripts.RcLocal), 0o755); err != nil {
			return fmt.Errorf("writing rc.local: %w", err)
		}
		// Execute rc.local via Incus exec (no need to restart).
		i.execSimple(ctx, full, []string{"bash", "/etc/rc.local"})
	}

	return nil
}

// pushEgressFiles writes all egress-related files into the container.
func (i *Incus) pushEgressFiles(ctx context.Context, full, egressMode string, allow []string) error {
	domains := egress.ResolveDomains(egressMode, allow)
	if err := i.pushFile(full, "/etc/pixels-egress-domains", []byte(egress.DomainsFileContent(domains)), 0o644); err != nil {
		return fmt.Errorf("writing egress domains: %w", err)
	}
	cidrs := egress.PresetCIDRs(egressMode)
	if len(cidrs) > 0 {
		if err := i.pushFile(full, "/etc/pixels-egress-cidrs", []byte(egress.CIDRsFileContent(cidrs)), 0o644); err != nil {
			return fmt.Errorf("writing egress cidrs: %w", err)
		}
	}
	if err := i.pushFile(full, "/etc/nftables.conf", []byte(egress.NftablesConf()), 0o644); err != nil {
		return fmt.Errorf("writing nftables.conf: %w", err)
	}
	if err := i.pushFile(full, "/usr/local/bin/pixels-resolve-egress.sh", []byte(egress.ResolveScript()), 0o755); err != nil {
		return fmt.Errorf("writing resolve script: %w", err)
	}
	if err := i.pushFile(full, "/usr/local/bin/safe-apt", []byte(egress.SafeAptScript()), 0o755); err != nil {
		return fmt.Errorf("writing safe-apt: %w", err)
	}
	if err := i.pushFile(full, "/etc/sudoers.d/pixel.restricted", []byte(egress.SudoersRestricted()), 0o440); err != nil {
		return fmt.Errorf("writing restricted sudoers: %w", err)
	}
	if err := i.pushFile(full, "/usr/local/bin/pixels-setup-egress.sh", []byte(scripts.SetupEgress), 0o755); err != nil {
		return fmt.Errorf("writing egress setup script: %w", err)
	}
	if err := i.pushFile(full, "/usr/local/bin/pixels-enable-egress.sh", []byte(scripts.EnableEgress), 0o755); err != nil {
		return fmt.Errorf("writing egress enable script: %w", err)
	}
	return nil
}

// waitAgentReady polls until the instance is running and the agent is ready.
func (i *Incus) waitAgentReady(ctx context.Context, full string, timeout time.Duration) error {
	return retry.Poll(ctx, time.Second, timeout, func(ctx context.Context) (bool, error) {
		state, _, err := i.server.GetInstanceState(full)
		if err != nil {
			return false, nil
		}
		return state.StatusCode == api.Running && state.Pid > 0, nil
	})
}

// Get returns a single instance by bare name.
func (i *Incus) Get(ctx context.Context, name string) (*sandbox.Instance, error) {
	full := prefixed(name)
	inst, _, err := i.server.GetInstance(full)
	if err != nil {
		return nil, fmt.Errorf("getting %s: %w", name, err)
	}

	state, _, err := i.server.GetInstanceState(full)
	if err != nil {
		return nil, fmt.Errorf("getting state for %s: %w", name, err)
	}

	return &sandbox.Instance{
		Name:      name,
		Status:    normalizeStatus(inst.Status),
		Addresses: extractIPv4(state),
	}, nil
}

// List returns all px- prefixed instances with the prefix stripped.
func (i *Incus) List(ctx context.Context) ([]sandbox.Instance, error) {
	instances, err := i.server.GetInstances(api.InstanceTypeContainer)
	if err != nil {
		return nil, fmt.Errorf("listing instances: %w", err)
	}

	var result []sandbox.Instance
	for _, inst := range instances {
		if !strings.HasPrefix(inst.Name, containerPrefix) {
			continue
		}
		si := sandbox.Instance{
			Name:   unprefixed(inst.Name),
			Status: normalizeStatus(inst.Status),
		}
		// Get addresses from instance state.
		if state, _, err := i.server.GetInstanceState(inst.Name); err == nil {
			si.Addresses = extractIPv4(state)
		}
		result = append(result, si)
	}
	return result, nil
}

// Start starts a stopped instance.
func (i *Incus) Start(ctx context.Context, name string) error {
	full := prefixed(name)
	op, err := i.server.UpdateInstanceState(full, api.InstanceStatePut{
		Action:  "start",
		Timeout: -1,
	}, "")
	if err != nil {
		return fmt.Errorf("starting %s: %w", name, err)
	}
	if err := op.WaitContext(ctx); err != nil {
		return fmt.Errorf("waiting for %s to start: %w", name, err)
	}
	return nil
}

// Stop stops a running instance.
func (i *Incus) Stop(ctx context.Context, name string) error {
	full := prefixed(name)
	op, err := i.server.UpdateInstanceState(full, api.InstanceStatePut{
		Action:  "stop",
		Timeout: 30,
		Force:   true,
	}, "")
	if err != nil {
		return fmt.Errorf("stopping %s: %w", name, err)
	}
	if err := op.WaitContext(ctx); err != nil {
		return fmt.Errorf("waiting for %s to stop: %w", name, err)
	}
	return nil
}

// Delete stops (if running) and deletes an instance.
func (i *Incus) Delete(ctx context.Context, name string) error {
	full := prefixed(name)

	// Best-effort stop.
	op, err := i.server.UpdateInstanceState(full, api.InstanceStatePut{
		Action:  "stop",
		Timeout: 30,
		Force:   true,
	}, "")
	if err == nil {
		_ = op.WaitContext(ctx)
	}

	// Delete with retry for storage release timing.
	if err := retry.Do(ctx, 3, 2*time.Second, func(ctx context.Context) error {
		delOp, err := i.server.DeleteInstance(full)
		if err != nil {
			return fmt.Errorf("deleting %s: %w", name, err)
		}
		return delOp.WaitContext(ctx)
	}); err != nil {
		return err
	}
	return nil
}

// CreateSnapshot creates a snapshot for the named instance.
func (i *Incus) CreateSnapshot(ctx context.Context, name, label string) error {
	full := prefixed(name)
	op, err := i.server.CreateInstanceSnapshot(full, api.InstanceSnapshotsPost{
		Name: label,
	})
	if err != nil {
		return fmt.Errorf("creating snapshot: %w", err)
	}
	return op.WaitContext(ctx)
}

// ListSnapshots returns all snapshots for the named instance.
func (i *Incus) ListSnapshots(ctx context.Context, name string) ([]sandbox.Snapshot, error) {
	full := prefixed(name)
	snaps, err := i.server.GetInstanceSnapshots(full)
	if err != nil {
		return nil, fmt.Errorf("listing snapshots: %w", err)
	}
	result := make([]sandbox.Snapshot, len(snaps))
	for idx, s := range snaps {
		result[idx] = sandbox.Snapshot{
			Label: s.Name,
			Size:  s.Size,
		}
	}
	return result, nil
}

// SnapshotExists reports whether a snapshot with the given label exists on
// the named instance. If the instance is not found it returns (false, nil)
// so callers do not need to disambiguate "no instance" from "no snapshot".
func (i *Incus) SnapshotExists(ctx context.Context, instanceName, label string) (bool, error) {
	snaps, err := i.ListSnapshots(ctx, instanceName)
	if err != nil {
		// Treat instance-not-found as "no snapshot exists" — non-fatal.
		if errors.Is(sandbox.WrapNotFound(err), sandbox.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	for _, s := range snaps {
		if s.Label == label {
			return true, nil
		}
	}
	return false, nil
}

// DeleteSnapshot deletes a snapshot by label.
func (i *Incus) DeleteSnapshot(ctx context.Context, name, label string) error {
	full := prefixed(name)
	op, err := i.server.DeleteInstanceSnapshot(full, label)
	if err != nil {
		return fmt.Errorf("deleting snapshot: %w", err)
	}
	return op.WaitContext(ctx)
}

// RestoreSnapshot rolls back to the given snapshot: stop, restore, start.
func (i *Incus) RestoreSnapshot(ctx context.Context, name, label string) error {
	full := prefixed(name)

	// Stop instance.
	if err := i.Stop(ctx, name); err != nil {
		return fmt.Errorf("stopping for restore: %w", err)
	}

	// Restore from snapshot.
	inst, etag, err := i.server.GetInstance(full)
	if err != nil {
		return fmt.Errorf("getting instance for restore: %w", err)
	}
	put := inst.Writable()
	put.Restore = label
	op, err := i.server.UpdateInstance(full, put, etag)
	if err != nil {
		return fmt.Errorf("restoring snapshot: %w", err)
	}
	if err := op.WaitContext(ctx); err != nil {
		return fmt.Errorf("waiting for restore: %w", err)
	}

	// Start instance.
	if err := i.Start(ctx, name); err != nil {
		return fmt.Errorf("starting after restore: %w", err)
	}

	return nil
}

// CloneFrom copies an instance from a source snapshot into a new instance.
func (i *Incus) CloneFrom(ctx context.Context, source, label, newName string) error {
	sourceFull := prefixed(source)
	newFull := prefixed(newName)

	// Get source instance for the copy.
	sourceInst, _, err := i.server.GetInstance(sourceFull)
	if err != nil {
		return fmt.Errorf("getting source instance: %w", err)
	}

	// Copy from snapshot.
	sourceInst.Name = sourceFull + "/" + label
	op, err := i.server.CopyInstance(i.server, *sourceInst, &incusclient.InstanceCopyArgs{
		Name:         newFull,
		InstanceOnly: true,
	})
	if err != nil {
		return fmt.Errorf("copying instance: %w", err)
	}
	if err := op.Wait(); err != nil {
		return fmt.Errorf("waiting for copy: %w", err)
	}

	return nil
}

// normalizeStatus converts Incus status strings ("Running") to sandbox.Status ("RUNNING").
func normalizeStatus(s string) sandbox.Status { return sandbox.Status(strings.ToUpper(s)) }

// extractIPv4 extracts all global IPv4 addresses from instance state.
func extractIPv4(state *api.InstanceState) []string {
	if state == nil || state.Network == nil {
		return nil
	}
	var addrs []string
	for name, net := range state.Network {
		if name == "lo" {
			continue
		}
		for _, addr := range net.Addresses {
			if addr.Family == "inet" && addr.Scope == "global" {
				addrs = append(addrs, addr.Address)
			}
		}
	}
	return addrs
}

// readSSHPubKey reads the .pub file corresponding to the given private key path.
func readSSHPubKey(keyPath string) string {
	if keyPath == "" {
		return ""
	}
	data, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// execSimple runs a command inside a container and waits for it to complete.
// Returns the exit code, ignoring errors for best-effort operations.
func (i *Incus) execSimple(ctx context.Context, full string, cmd []string) int {
	dataDone := make(chan bool)
	args := &incusclient.InstanceExecArgs{
		Stdout:   io.Discard,
		Stderr:   io.Discard,
		DataDone: dataDone,
	}

	op, err := i.server.ExecInstance(full, api.InstanceExecPost{
		Command:     cmd,
		WaitForWS:   true,
		Interactive: false,
	}, args)
	if err != nil {
		return 1
	}
	if err := op.WaitContext(ctx); err != nil {
		return 1
	}
	<-dataDone

	return exitCodeFromOp(op)
}
