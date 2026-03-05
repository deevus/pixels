package truenas

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tnapi "github.com/deevus/truenas-go"

	"github.com/deevus/pixels/internal/provision"
	"github.com/deevus/pixels/internal/retry"
	"github.com/deevus/pixels/internal/ssh"
	"github.com/deevus/pixels/sandbox"
)

// Create creates a new container instance with the full provisioning flow:
// NIC resolution, instance creation, provisioning, restart, IP poll, SSH wait.
// When opts.Bare is true, only the instance is created (no provisioning or SSH wait).
func (t *TrueNAS) Create(ctx context.Context, opts sandbox.CreateOpts) (*sandbox.Instance, error) {
	name := opts.Name
	full := prefixed(name)

	image := opts.Image
	if image == "" {
		image = t.cfg.image
	}
	cpu := opts.CPU
	if cpu == "" {
		cpu = t.cfg.cpu
	}
	memory := opts.Memory
	if memory == 0 {
		memory = t.cfg.memory * 1024 * 1024 // MiB → bytes
	}

	createOpts := CreateInstanceOpts{
		Name:      full,
		Image:     image,
		CPU:       cpu,
		Memory:    memory,
		Autostart: true,
	}

	// Resolve NIC: config override or auto-detect.
	if t.cfg.nicType != "" {
		createOpts.NIC = &NICOpts{
			NICType: strings.ToUpper(t.cfg.nicType),
			Parent:  t.cfg.parent,
		}
	} else {
		nic, err := t.client.DefaultNIC(ctx)
		if err == nil {
			createOpts.NIC = nic
		}
	}

	instance, err := t.client.CreateInstance(ctx, createOpts)
	if err != nil {
		return nil, fmt.Errorf("creating instance: %w", err)
	}

	// Bare mode: return immediately without provisioning or waiting.
	if opts.Bare {
		return &sandbox.Instance{
			Name:      name,
			Status:    sandbox.Status(instance.Status),
			Addresses: collectAddresses(instance.Aliases),
		}, nil
	}

	// Provision if enabled.
	if t.cfg.provision {
		pubKey := readSSHPubKey(t.cfg.sshKey)
		steps := provision.Steps(t.cfg.egress, t.cfg.devtools)

		provOpts := ProvisionOpts{
			SSHPubKey:   pubKey,
			DNS:         t.cfg.dns,
			Env:         t.cfg.env,
			DevTools:    t.cfg.devtools,
			Egress:      t.cfg.egress,
			EgressAllow: t.cfg.allow,
		}
		if len(steps) > 0 {
			provOpts.ProvisionScript = provision.Script(steps)
		}

		needsProvision := pubKey != "" || len(t.cfg.dns) > 0 ||
			len(t.cfg.env) > 0 || t.cfg.devtools

		if needsProvision {
			if err := t.client.Provision(ctx, full, provOpts); err != nil {
				t.warnf("provision %s: %v", name, err)
			} else if pubKey != "" {
				// Restart so rc.local runs on boot.
				_ = t.client.Virt.StopInstance(ctx, full, tnapi.StopVirtInstanceOpts{Timeout: 30})
				if err := t.client.Virt.StartInstance(ctx, full); err != nil {
					return nil, fmt.Errorf("restarting after provision: %w", err)
				}
				instance = nil // force re-fetch below
			}
		}
	}

	// Poll for IP assignment.
	if err := retry.Poll(ctx, time.Second, 15*time.Second, func(ctx context.Context) (bool, error) {
		inst, err := t.client.Virt.GetInstance(ctx, full)
		if err != nil {
			return false, fmt.Errorf("refreshing instance: %w", err)
		}
		instance = inst
		return ipFromAliases(inst.Aliases) != "", nil
	}); err != nil && !errors.Is(err, retry.ErrTimeout) {
		return nil, err
	}

	ip := ipFromAliases(instance.Aliases)

	// Wait for SSH readiness.
	if ip != "" {
		// Remove stale known_hosts entries — the container was just created.
		ssh.RemoveKnownHost(t.cfg.knownHosts, ip)
		ssh.RemoveKnownHost(t.cfg.knownHosts, full)
		if err := t.ssh.WaitReady(ctx, full, 90*time.Second, nil); err != nil {
			t.warnf("ssh wait %s: %v", name, err)
		}
	}

	return &sandbox.Instance{
		Name:      name,
		Status:    sandbox.Status(instance.Status),
		Addresses: collectAddresses(instance.Aliases),
	}, nil
}

// Get returns a single instance by bare name.
func (t *TrueNAS) Get(ctx context.Context, name string) (*sandbox.Instance, error) {
	inst, err := t.client.Virt.GetInstance(ctx, prefixed(name))
	if err != nil {
		return nil, fmt.Errorf("getting %s: %w", name, err)
	}
	if inst == nil {
		return nil, fmt.Errorf("instance %q not found", name)
	}
	return toInstance(inst), nil
}

// List returns all px- prefixed instances with the prefix stripped.
func (t *TrueNAS) List(ctx context.Context) ([]sandbox.Instance, error) {
	instances, err := t.client.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]sandbox.Instance, len(instances))
	for i, inst := range instances {
		result[i] = sandbox.Instance{
			Name:      unprefixed(inst.Name),
			Status:    sandbox.Status(inst.Status),
			Addresses: collectAddresses(inst.Aliases),
		}
	}
	return result, nil
}

// Start starts a stopped instance.
func (t *TrueNAS) Start(ctx context.Context, name string) error {
	full := prefixed(name)
	if err := t.client.Virt.StartInstance(ctx, full); err != nil {
		return fmt.Errorf("starting %s: %w", name, err)
	}

	// Get instance and wait for SSH.
	inst, err := t.client.Virt.GetInstance(ctx, full)
	if err != nil {
		return fmt.Errorf("refreshing %s: %w", name, err)
	}

	ip := ipFromAliases(inst.Aliases)
	if ip != "" {
		// Remove stale known_hosts entries — host key may differ after restart.
		ssh.RemoveKnownHost(t.cfg.knownHosts, ip)
		ssh.RemoveKnownHost(t.cfg.knownHosts, full)
		if err := t.ssh.WaitReady(ctx, full, 30*time.Second, nil); err != nil {
			t.warnf("ssh wait %s: %v", name, err)
		}
	}
	return nil
}

// Stop stops a running instance.
func (t *TrueNAS) Stop(ctx context.Context, name string) error {
	if err := t.client.Virt.StopInstance(ctx, prefixed(name), tnapi.StopVirtInstanceOpts{
		Timeout: 30,
	}); err != nil {
		return fmt.Errorf("stopping %s: %w", name, err)
	}
	return nil
}

// Delete stops (if running) and deletes an instance with retry.
func (t *TrueNAS) Delete(ctx context.Context, name string) error {
	full := prefixed(name)

	// Best-effort stop.
	_ = t.client.Virt.StopInstance(ctx, full, tnapi.StopVirtInstanceOpts{Timeout: 30})

	// Resolve IP before deletion so we can clean up the known_hosts entry.
	inst, _ := t.client.Virt.GetInstance(ctx, full)
	var ip string
	if inst != nil {
		ip = ipFromAliases(inst.Aliases)
	}

	// Retry delete (Incus storage release timing).
	if err := retry.Do(ctx, 3, 2*time.Second, func(ctx context.Context) error {
		return t.client.Virt.DeleteInstance(ctx, full)
	}); err != nil {
		return fmt.Errorf("deleting %s: %w", name, err)
	}

	// Clean up known_hosts entries for the now-dead container.
	if ip != "" {
		ssh.RemoveKnownHost(t.cfg.knownHosts, ip)
	}
	ssh.RemoveKnownHost(t.cfg.knownHosts, full)
	return nil
}

// CreateSnapshot creates a ZFS snapshot for the named instance.
func (t *TrueNAS) CreateSnapshot(ctx context.Context, name, label string) error {
	ds, err := t.resolveDataset(ctx, name)
	if err != nil {
		return err
	}
	_, err = t.client.Snapshot.Create(ctx, tnapi.CreateSnapshotOpts{
		Dataset: ds,
		Name:    label,
	})
	if err != nil {
		return fmt.Errorf("creating snapshot: %w", err)
	}
	return nil
}

// ListSnapshots returns all snapshots for the named instance.
func (t *TrueNAS) ListSnapshots(ctx context.Context, name string) ([]sandbox.Snapshot, error) {
	ds, err := t.resolveDataset(ctx, name)
	if err != nil {
		return nil, err
	}
	snaps, err := t.client.ListSnapshots(ctx, ds)
	if err != nil {
		return nil, err
	}
	result := make([]sandbox.Snapshot, len(snaps))
	for i, s := range snaps {
		result[i] = sandbox.Snapshot{
			Label: s.SnapshotName,
			Size:  s.Referenced,
		}
	}
	return result, nil
}

// DeleteSnapshot deletes a ZFS snapshot by label.
func (t *TrueNAS) DeleteSnapshot(ctx context.Context, name, label string) error {
	ds, err := t.resolveDataset(ctx, name)
	if err != nil {
		return err
	}
	return t.client.Snapshot.Delete(ctx, ds+"@"+label)
}

// RestoreSnapshot rolls back to the given snapshot: stop, rollback, start,
// poll IP, SSH wait.
func (t *TrueNAS) RestoreSnapshot(ctx context.Context, name, label string) error {
	full := prefixed(name)
	ds, err := t.resolveDataset(ctx, name)
	if err != nil {
		return err
	}

	if err := t.client.Virt.StopInstance(ctx, full, tnapi.StopVirtInstanceOpts{Timeout: 30}); err != nil {
		return fmt.Errorf("stopping %s: %w", name, err)
	}
	if err := t.client.SnapshotRollback(ctx, ds+"@"+label); err != nil {
		return err
	}
	if err := t.client.Virt.StartInstance(ctx, full); err != nil {
		return fmt.Errorf("starting %s: %w", name, err)
	}

	inst, err := t.client.Virt.GetInstance(ctx, full)
	if err != nil {
		return fmt.Errorf("refreshing %s: %w", name, err)
	}

	ip := ipFromAliases(inst.Aliases)
	if ip != "" {
		// Remove stale known_hosts entries — snapshot restore changes the host key.
		ssh.RemoveKnownHost(t.cfg.knownHosts, ip)
		ssh.RemoveKnownHost(t.cfg.knownHosts, full)
		if err := t.ssh.WaitReady(ctx, full, 30*time.Second, nil); err != nil {
			t.warnf("ssh wait %s: %v", name, err)
		}
	}
	return nil
}

// CloneFrom clones a source container's snapshot into a new container.
func (t *TrueNAS) CloneFrom(ctx context.Context, source, label, newName string) error {
	ds, err := t.resolveDataset(ctx, source)
	if err != nil {
		return err
	}
	return t.client.ReplaceContainerRootfs(ctx, prefixed(newName), ds+"@"+label)
}

// resolveDataset returns the ZFS dataset path for an instance.
func (t *TrueNAS) resolveDataset(ctx context.Context, name string) (string, error) {
	if t.cfg.datasetPrefix != "" {
		return t.cfg.datasetPrefix + "/" + prefixed(name), nil
	}
	return t.client.ContainerDataset(ctx, prefixed(name))
}

// toInstance converts a truenas-go VirtInstance to a sandbox.Instance.
func toInstance(inst *tnapi.VirtInstance) *sandbox.Instance {
	return &sandbox.Instance{
		Name:      unprefixed(inst.Name),
		Status:    sandbox.Status(inst.Status),
		Addresses: collectAddresses(inst.Aliases),
	}
}

// collectAddresses extracts all IPv4 addresses from aliases.
func collectAddresses(aliases []tnapi.VirtAlias) []string {
	var addrs []string
	for _, a := range aliases {
		if a.Type == "INET" || a.Type == "ipv4" {
			addrs = append(addrs, a.Address)
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
