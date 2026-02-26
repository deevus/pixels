package truenas

import (
	"context"
	"fmt"
	"net"
	"strings"

	truenas "github.com/deevus/truenas-go"
	"github.com/deevus/truenas-go/client"

	"github.com/deevus/pixels/internal/config"
)

// Client wraps a truenas-go WebSocket client and its typed services.
type Client struct {
	ws         client.Client
	Virt       truenas.VirtServiceAPI
	Snapshot   truenas.SnapshotServiceAPI
	Interface  truenas.InterfaceServiceAPI
	Network    truenas.NetworkServiceAPI
	Filesystem truenas.FilesystemServiceAPI
}

// Connect creates and connects a TrueNAS WebSocket client.
func Connect(ctx context.Context, cfg *config.Config) (*Client, error) {
	ws, err := client.NewWebSocketClient(client.WebSocketConfig{
		Host:               cfg.TrueNAS.Host,
		Port:               cfg.TrueNAS.Port,
		Username:           cfg.TrueNAS.Username,
		APIKey:             cfg.TrueNAS.APIKey,
		InsecureSkipVerify: cfg.TrueNAS.InsecureSkipVerifyValue(),
	})
	if err != nil {
		return nil, fmt.Errorf("creating client: %w", err)
	}

	if err := ws.Connect(ctx); err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", cfg.TrueNAS.Host, err)
	}

	v := ws.Version()
	return &Client{
		ws:         ws,
		Virt:       truenas.NewVirtService(ws, v),
		Snapshot:   truenas.NewSnapshotService(ws, v),
		Interface:  truenas.NewInterfaceService(ws, v),
		Network:    truenas.NewNetworkService(ws, v),
		Filesystem: truenas.NewFilesystemService(ws, v),
	}, nil
}

func (c *Client) Close() error {
	return c.ws.Close()
}

// ContainerDataset returns the ZFS dataset path for a container by name.
func (c *Client) ContainerDataset(ctx context.Context, name string) (string, error) {
	gcfg, err := c.Virt.GetGlobalConfig(ctx)
	if err != nil {
		return "", fmt.Errorf("querying virt global config: %w", err)
	}
	if gcfg.Dataset == "" {
		return "", fmt.Errorf("no dataset in virt global config")
	}
	return gcfg.Dataset + "/containers/" + name, nil
}

// ProvisionOpts contains options for provisioning a container.
type ProvisionOpts struct {
	SSHPubKey string
	DNS       []string // nameservers (e.g. ["1.1.1.1", "8.8.8.8"])
}

// Provision writes SSH keys, rc.local for openssh-server install, and
// optional DNS config into a running container's rootfs via file_receive.
func (c *Client) Provision(ctx context.Context, name string, opts ProvisionOpts) error {
	gcfg, err := c.Virt.GetGlobalConfig(ctx)
	if err != nil {
		return err
	}
	if gcfg.Pool == "" {
		return fmt.Errorf("no pool in virt global config")
	}
	// Container rootfs on the TrueNAS host filesystem.
	rootfs := fmt.Sprintf("/var/lib/incus/storage-pools/%s/containers/%s/rootfs", gcfg.Pool, name)

	// Configure upstream DNS for systemd-resolved via drop-in.
	// /etc/resolv.conf is a symlink managed by systemd-resolved, so we
	// configure it through resolved.conf.d instead.
	if len(opts.DNS) > 0 {
		var conf strings.Builder
		conf.WriteString("[Resolve]\nDNS=")
		conf.WriteString(strings.Join(opts.DNS, " "))
		conf.WriteString("\n")
		dropinPath := rootfs + "/etc/systemd/resolved.conf.d/pixels-dns.conf"
		if err := c.Filesystem.WriteFile(ctx, dropinPath, truenas.WriteFileParams{
			Content: []byte(conf.String()),
			Mode:    0o644,
		}); err != nil {
			return fmt.Errorf("writing resolved drop-in: %w", err)
		}
	}

	if opts.SSHPubKey == "" {
		return nil
	}

	// Write authorized_keys.
	// filesystem.mkdir blocks incus paths, but file_receive works and
	// auto-creates parent directories.
	sshDir := rootfs + "/root/.ssh"
	if err := c.Filesystem.WriteFile(ctx, sshDir+"/authorized_keys", truenas.WriteFileParams{
		Content: []byte(opts.SSHPubKey + "\n"),
		Mode:    0o600,
	}); err != nil {
		return fmt.Errorf("writing authorized_keys: %w", err)
	}

	// Write rc.local — systemd-rc-local-generator automatically creates and
	// starts rc-local.service if /etc/rc.local exists and is executable.
	rcLocal := `#!/bin/sh
set -e
if [ ! -f /root/.ssh-provisioned ]; then
    apt-get update -qq
    apt-get install -y -qq openssh-server sudo

    if ! id pixel >/dev/null 2>&1; then
        useradd -m -s /bin/bash -G sudo pixel
    fi

    echo 'pixel ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/pixel
    chmod 0440 /etc/sudoers.d/pixel

    mkdir -p /home/pixel/.ssh
    cp /root/.ssh/authorized_keys /home/pixel/.ssh/authorized_keys
    chown -R pixel:pixel /home/pixel/.ssh
    chmod 700 /home/pixel/.ssh
    chmod 600 /home/pixel/.ssh/authorized_keys

    systemctl enable --now ssh
    touch /root/.ssh-provisioned
fi
exit 0
`
	if err := c.Filesystem.WriteFile(ctx, rootfs+"/etc/rc.local", truenas.WriteFileParams{
		Content: []byte(rcLocal),
		Mode:    0o755,
	}); err != nil {
		return fmt.Errorf("writing rc.local: %w", err)
	}

	return nil
}

// NICOpts describes a NIC device to attach during container creation.
type NICOpts struct {
	NICType string // "MACVLAN" or "BRIDGED"
	Parent  string // host interface (e.g. "eno1")
}

// DefaultNIC discovers the host's gateway interface and returns NIC options
// suitable for container creation. It queries TrueNAS for the default IPv4
// gateway, then finds the physical interface whose subnet contains that
// gateway. Falls back to the first physical interface that is UP with an
// IPv4 address.
func (c *Client) DefaultNIC(ctx context.Context) (*NICOpts, error) {
	ifaces, err := c.Interface.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing interfaces: %w", err)
	}

	// Filter to physical interfaces that are UP with an IPv4 address.
	type candidate struct {
		name    string
		address string
		netmask int
	}
	var candidates []candidate
	for _, iface := range ifaces {
		if iface.Type != truenas.InterfaceTypePhysical {
			continue
		}
		if iface.State.LinkState != truenas.LinkStateUp {
			continue
		}
		for _, alias := range iface.Aliases {
			if alias.Type == truenas.AliasTypeINET {
				candidates = append(candidates, candidate{
					name:    iface.Name,
					address: alias.Address,
					netmask: alias.Netmask,
				})
				break
			}
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no physical interface with IPv4 found")
	}

	// Try to match the default gateway to an interface subnet.
	if gw := c.defaultGateway(ctx); gw != nil {
		for _, cand := range candidates {
			ip := net.ParseIP(cand.address)
			if ip == nil {
				continue
			}
			mask := net.CIDRMask(cand.netmask, 32)
			network := &net.IPNet{IP: ip.Mask(mask), Mask: mask}
			if network.Contains(gw) {
				return &NICOpts{NICType: "MACVLAN", Parent: cand.name}, nil
			}
		}
	}

	// Fallback: first candidate.
	return &NICOpts{NICType: "MACVLAN", Parent: candidates[0].name}, nil
}

// defaultGateway queries network.general.summary for the default IPv4 gateway.
// Returns nil if the gateway cannot be determined.
func (c *Client) defaultGateway(ctx context.Context) net.IP {
	summary, err := c.Network.GetSummary(ctx)
	if err != nil {
		return nil
	}
	for _, route := range summary.DefaultRoutes {
		if ip := net.ParseIP(route); ip != nil && ip.To4() != nil {
			return ip
		}
	}
	return nil
}

// CreateInstanceOpts contains options for creating a container.
type CreateInstanceOpts struct {
	Name      string
	Image     string
	CPU       string
	Memory    int64 // bytes
	Autostart bool
	NIC *NICOpts
}

// CreateInstance creates an Incus container via the Virt service.
func (c *Client) CreateInstance(ctx context.Context, opts CreateInstanceOpts) (*truenas.VirtInstance, error) {
	createOpts := truenas.CreateVirtInstanceOpts{
		Name:         opts.Name,
		InstanceType: "CONTAINER",
		Image:        opts.Image,
		CPU:          opts.CPU,
		Memory:       opts.Memory,
		Autostart:    opts.Autostart,
	}
	if opts.NIC != nil {
		createOpts.Devices = []truenas.VirtDeviceOpts{{
			DevType: "NIC",
			NICType: opts.NIC.NICType,
			Parent:  opts.NIC.Parent,
		}}
	}
	return c.Virt.CreateInstance(ctx, createOpts)
}

// ListInstances queries all Incus instances with the px- prefix.
func (c *Client) ListInstances(ctx context.Context) ([]truenas.VirtInstance, error) {
	return c.Virt.ListInstances(ctx, [][]any{{"name", "^", "px-"}})
}

// ListSnapshots queries snapshots for the given ZFS dataset.
func (c *Client) ListSnapshots(ctx context.Context, dataset string) ([]truenas.Snapshot, error) {
	return c.Snapshot.Query(ctx, [][]any{{"dataset", "=", dataset}})
}

// SnapshotRollback rolls back to the given snapshot ID (dataset@name).
func (c *Client) SnapshotRollback(ctx context.Context, snapshotID string) error {
	return c.Snapshot.Rollback(ctx, snapshotID)
}
