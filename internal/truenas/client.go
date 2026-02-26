package truenas

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	truenas "github.com/deevus/truenas-go"
	"github.com/deevus/truenas-go/client"

	"github.com/deevus/pixels/internal/config"
)

// Client wraps a truenas-go WebSocket client and its services,
// adding raw API call wrappers for methods not yet in truenas-go.
type Client struct {
	ws        client.Client
	Virt      truenas.VirtServiceAPI
	Snapshot  truenas.SnapshotServiceAPI
	Interface truenas.InterfaceServiceAPI
	version   truenas.Version
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
		ws:        ws,
		Virt:      truenas.NewVirtService(ws, v),
		Snapshot:  truenas.NewSnapshotService(ws, v),
		Interface: truenas.NewInterfaceService(ws, v),
		version:   v,
	}, nil
}

func (c *Client) Close() error {
	return c.ws.Close()
}

func (c *Client) Version() truenas.Version {
	return c.version
}

type virtGlobalConfig struct {
	Pool         string   `json:"pool"`
	Dataset      string   `json:"dataset"`
	StoragePools []string `json:"storage_pools"`
}

func (c *Client) getVirtGlobalConfig(ctx context.Context) (*virtGlobalConfig, error) {
	result, err := c.ws.Call(ctx, "virt.global.config", nil)
	if err != nil {
		return nil, fmt.Errorf("querying virt global config: %w", err)
	}
	var gcfg virtGlobalConfig
	if err := json.Unmarshal(result, &gcfg); err != nil {
		return nil, fmt.Errorf("parsing virt global config: %w", err)
	}
	return &gcfg, nil
}

// ContainerDataset returns the ZFS dataset path for a container by name.
func (c *Client) ContainerDataset(ctx context.Context, name string) (string, error) {
	gcfg, err := c.getVirtGlobalConfig(ctx)
	if err != nil {
		return "", err
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
	gcfg, err := c.getVirtGlobalConfig(ctx)
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
		if err := c.ws.WriteFile(ctx, dropinPath, truenas.WriteFileParams{
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
	if err := c.ws.WriteFile(ctx, sshDir+"/authorized_keys", truenas.WriteFileParams{
		Content: []byte(opts.SSHPubKey + "\n"),
		Mode:    0o600,
	}); err != nil {
		return fmt.Errorf("writing authorized_keys: %w", err)
	}

	// Write rc.local — systemd-rc-local-generator automatically creates and
	// starts rc-local.service if /etc/rc.local exists and is executable.
	rcLocal := `#!/bin/sh
if [ ! -f /root/.ssh-provisioned ]; then
    apt-get update -qq && apt-get install -y -qq openssh-server && systemctl enable --now ssh && touch /root/.ssh-provisioned
fi
exit 0
`
	if err := c.ws.WriteFile(ctx, rootfs+"/etc/rc.local", truenas.WriteFileParams{
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
	result, err := c.ws.Call(ctx, "network.general.summary", nil)
	if err != nil {
		return nil
	}
	var summary struct {
		DefaultRoutes []string `json:"default_routes"`
	}
	if err := json.Unmarshal(result, &summary); err != nil {
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

// CreateInstance creates an Incus container via raw API call.
// This bypasses truenas-go's VirtDeviceOpts which sends empty strings
// for null-able NIC fields (network), causing validation errors.
func (c *Client) CreateInstance(ctx context.Context, opts CreateInstanceOpts) (*truenas.VirtInstance, error) {
	params := map[string]any{
		"name":          opts.Name,
		"instance_type": "CONTAINER",
		"image":         opts.Image,
		"cpu":           opts.CPU,
		"memory":        opts.Memory,
		"autostart":     opts.Autostart,
	}

	if opts.NIC != nil {
		nic := map[string]any{
			"dev_type": "NIC",
			"nic_type": opts.NIC.NICType,
			"parent":   opts.NIC.Parent,
		}
		params["devices"] = []map[string]any{nic}
	}

	result, err := c.ws.CallAndWait(ctx, "virt.instance.create", []any{params})
	if err != nil {
		return nil, err
	}

	var instance truenas.VirtInstance
	if err := json.Unmarshal(result, &instance); err != nil {
		return nil, fmt.Errorf("parsing created instance: %w", err)
	}
	return &instance, nil
}

// ListInstances queries all Incus instances with the px- prefix.
func (c *Client) ListInstances(ctx context.Context) ([]truenas.VirtInstance, error) {
	result, err := c.ws.Call(ctx, "virt.instance.query", []any{
		[][]any{{"name", "^", "px-"}},
	})
	if err != nil {
		return nil, fmt.Errorf("querying instances: %w", err)
	}

	var instances []truenas.VirtInstance
	if err := json.Unmarshal(result, &instances); err != nil {
		return nil, fmt.Errorf("parsing instance list: %w", err)
	}
	return instances, nil
}

// ListSnapshots queries snapshots for the given ZFS dataset.
func (c *Client) ListSnapshots(ctx context.Context, dataset string) ([]truenas.Snapshot, error) {
	method := "zfs.snapshot.query"
	if c.version.AtLeast(25, 10) {
		method = "pool.snapshot.query"
	}

	result, err := c.ws.Call(ctx, method, []any{
		[][]any{{"dataset", "=", dataset}},
	})
	if err != nil {
		return nil, fmt.Errorf("querying snapshots: %w", err)
	}

	var snapshots []truenas.Snapshot
	if err := json.Unmarshal(result, &snapshots); err != nil {
		return nil, fmt.Errorf("parsing snapshot list: %w", err)
	}
	return snapshots, nil
}

// SnapshotRollback rolls back to the given snapshot ID (dataset@name).
func (c *Client) SnapshotRollback(ctx context.Context, snapshotID string) error {
	method := "zfs.snapshot.rollback"
	if c.version.AtLeast(25, 10) {
		method = "pool.snapshot.rollback"
	}

	_, err := c.ws.Call(ctx, method, []any{snapshotID})
	if err != nil {
		return fmt.Errorf("rolling back snapshot %s: %w", snapshotID, err)
	}
	return nil
}
