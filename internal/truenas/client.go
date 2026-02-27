package truenas

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"strings"
	"text/template"

	truenas "github.com/deevus/truenas-go"
	"github.com/deevus/truenas-go/client"

	"github.com/deevus/pixels/internal/config"
	"github.com/deevus/pixels/internal/egress"
)

// Client wraps a truenas-go WebSocket client and its typed services.
type Client struct {
	ws         client.Client
	Virt       truenas.VirtServiceAPI
	Snapshot   truenas.SnapshotServiceAPI
	Interface  truenas.InterfaceServiceAPI
	Network    truenas.NetworkServiceAPI
	Filesystem truenas.FilesystemServiceAPI
	Cron       truenas.CronServiceAPI
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
		Cron:       truenas.NewCronService(ws, v),
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

// WriteContainerFile writes a file into a running container's rootfs via the
// TrueNAS filesystem API (no SSH required).
func (c *Client) WriteContainerFile(ctx context.Context, name, path string, content []byte, mode fs.FileMode) error {
	gcfg, err := c.Virt.GetGlobalConfig(ctx)
	if err != nil {
		return fmt.Errorf("querying virt global config: %w", err)
	}
	if gcfg.Pool == "" {
		return fmt.Errorf("no pool in virt global config")
	}
	rootfs := fmt.Sprintf("/var/lib/incus/storage-pools/%s/containers/%s/rootfs", gcfg.Pool, name)
	return c.Filesystem.WriteFile(ctx, rootfs+path, truenas.WriteFileParams{
		Content: content,
		Mode:    mode,
	})
}

// ProvisionOpts contains options for provisioning a container.
type ProvisionOpts struct {
	SSHPubKey   string
	DNS         []string          // nameservers (e.g. ["1.1.1.1", "8.8.8.8"])
	Env         map[string]string // environment variables to inject into /etc/environment
	DevTools    bool              // whether to install dev tools (mise, claude-code, codex, opencode)
	Egress      string            // "unrestricted", "agent", or "allowlist"
	EgressAllow []string          // custom domains (merged into agent, standalone for allowlist)
	Log         io.Writer         // optional; verbose progress output
}

// Provision writes SSH keys, rc.local for openssh-server install, dev tools
// setup, and optional DNS/env config into a running container's rootfs via
// file_receive.
func (c *Client) Provision(ctx context.Context, name string, opts ProvisionOpts) error {
	logf := func(format string, a ...any) {
		if opts.Log != nil {
			fmt.Fprintf(opts.Log, format+"\n", a...)
		}
	}

	gcfg, err := c.Virt.GetGlobalConfig(ctx)
	if err != nil {
		return err
	}
	if gcfg.Pool == "" {
		return fmt.Errorf("no pool in virt global config")
	}
	// Container rootfs on the TrueNAS host filesystem.
	rootfs := fmt.Sprintf("/var/lib/incus/storage-pools/%s/containers/%s/rootfs", gcfg.Pool, name)
	logf("Rootfs: %s", rootfs)

	// Configure upstream DNS for systemd-resolved via drop-in.
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
		logf("Wrote DNS config (%d nameservers)", len(opts.DNS))
	}

	// Write environment variables to /etc/environment (sourced by PAM on login).
	if len(opts.Env) > 0 {
		var envBuf strings.Builder
		for k, v := range opts.Env {
			fmt.Fprintf(&envBuf, "%s=%q\n", k, v)
		}
		if err := c.Filesystem.WriteFile(ctx, rootfs+"/etc/environment", truenas.WriteFileParams{
			Content: []byte(envBuf.String()),
			Mode:    0o644,
		}); err != nil {
			return fmt.Errorf("writing /etc/environment: %w", err)
		}
		logf("Wrote /etc/environment (%d vars)", len(opts.Env))
	}

	if opts.SSHPubKey == "" && !opts.DevTools {
		return nil
	}

	// Write authorized_keys for both root and pixel user.
	// Writing to pixel's home now (via file_receive) ensures the key is
	// available immediately, before rc.local creates the user and chowns it.
	// Pixel user is created with UID/GID 1000 by rc.local.
	if opts.SSHPubKey != "" {
		keyData := []byte(opts.SSHPubKey + "\n")
		if err := c.Filesystem.WriteFile(ctx, rootfs+"/root/.ssh/authorized_keys", truenas.WriteFileParams{
			Content: keyData,
			Mode:    0o600,
		}); err != nil {
			return fmt.Errorf("writing authorized_keys: %w", err)
		}
		pixelUID := intPtr(1000)
		if err := c.Filesystem.WriteFile(ctx, rootfs+"/home/pixel/.ssh/authorized_keys", truenas.WriteFileParams{
			Content: keyData,
			Mode:    0o600,
			UID:     pixelUID,
			GID:     pixelUID,
		}); err != nil {
			return fmt.Errorf("writing authorized_keys: %w", err)
		}
		logf("Wrote SSH authorized_keys (root + pixel)")
	}

	// Write dev tools setup script and systemd unit.
	if opts.DevTools {
		if err := c.Filesystem.WriteFile(ctx, rootfs+"/usr/local/bin/pixels-setup-devtools.sh", truenas.WriteFileParams{
			Content: []byte(devtoolsSetupScript),
			Mode:    0o755,
		}); err != nil {
			return fmt.Errorf("writing devtools setup script: %w", err)
		}
		if err := c.Filesystem.WriteFile(ctx, rootfs+"/etc/systemd/system/pixels-devtools.service", truenas.WriteFileParams{
			Content: []byte(devtoolsServiceUnit),
			Mode:    0o644,
		}); err != nil {
			return fmt.Errorf("writing devtools systemd unit: %w", err)
		}
		logf("Wrote devtools setup script + systemd unit")
	}

	// Write egress control files when egress mode is restricted.
	isRestricted := opts.Egress == "agent" || opts.Egress == "allowlist"
	if isRestricted {
		domains := egress.ResolveDomains(opts.Egress, opts.EgressAllow)
		if err := c.Filesystem.WriteFile(ctx, rootfs+"/etc/pixels-egress-domains", truenas.WriteFileParams{
			Content: []byte(egress.DomainsFileContent(domains)),
			Mode:    0o644,
		}); err != nil {
			return fmt.Errorf("writing egress domains: %w", err)
		}
		cidrs := egress.PresetCIDRs(opts.Egress)
		if len(cidrs) > 0 {
			if err := c.Filesystem.WriteFile(ctx, rootfs+"/etc/pixels-egress-cidrs", truenas.WriteFileParams{
				Content: []byte(egress.CIDRsFileContent(cidrs)),
				Mode:    0o644,
			}); err != nil {
				return fmt.Errorf("writing egress cidrs: %w", err)
			}
		}
		if err := c.Filesystem.WriteFile(ctx, rootfs+"/etc/nftables.conf", truenas.WriteFileParams{
			Content: []byte(egress.NftablesConf()),
			Mode:    0o644,
		}); err != nil {
			return fmt.Errorf("writing nftables.conf: %w", err)
		}
		if err := c.Filesystem.WriteFile(ctx, rootfs+"/usr/local/bin/pixels-resolve-egress.sh", truenas.WriteFileParams{
			Content: []byte(egress.ResolveScript()),
			Mode:    0o755,
		}); err != nil {
			return fmt.Errorf("writing egress resolve script: %w", err)
		}
		if err := c.Filesystem.WriteFile(ctx, rootfs+"/usr/local/bin/safe-apt", truenas.WriteFileParams{
			Content: []byte(egress.SafeAptScript()),
			Mode:    0o755,
		}); err != nil {
			return fmt.Errorf("writing safe-apt wrapper: %w", err)
		}
		if err := c.Filesystem.WriteFile(ctx, rootfs+"/etc/sudoers.d/pixel", truenas.WriteFileParams{
			Content: []byte(egress.SudoersRestricted()),
			Mode:    0o440,
		}); err != nil {
			return fmt.Errorf("writing restricted sudoers: %w", err)
		}
		logf("Wrote egress files (%d domains, %d cidrs, restricted sudoers)", len(domains), len(cidrs))
	}

	// Write rc.local — systemd-rc-local-generator automatically creates and
	// starts rc-local.service if /etc/rc.local exists and is executable.
	if opts.SSHPubKey != "" {
		rcLocal := buildRCLocal(isRestricted, opts.DevTools)
		if err := c.Filesystem.WriteFile(ctx, rootfs+"/etc/rc.local", truenas.WriteFileParams{
			Content: []byte(rcLocal),
			Mode:    0o755,
		}); err != nil {
			return fmt.Errorf("writing rc.local: %w", err)
		}
		logf("Wrote rc.local (egress=%v, devtools=%v)", isRestricted, opts.DevTools)
	}

	return nil
}

// rcLocalParams controls the rc.local template output.
type rcLocalParams struct {
	Egress   bool
	DevTools bool
}

var rcLocalTmpl = template.Must(template.New("rc.local").Parse(`#!/bin/sh
set -e
if [ ! -f /root/.ssh-provisioned ]; then
    apt-get update -qq
    apt-get install -y -qq openssh-server sudo

    if ! id pixel >/dev/null 2>&1; then
        userdel -r ubuntu 2>/dev/null || true
        groupdel ubuntu 2>/dev/null || true
        groupadd -g 1000 pixel
        useradd -m -u 1000 -g 1000 -s /bin/bash -G sudo pixel
    fi
    cp -rn /etc/skel/. /home/pixel/
    mkdir -p /home/pixel/.ssh
{{- if not .Egress}}

    echo 'pixel ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/pixel
    chmod 0440 /etc/sudoers.d/pixel
{{- end}}

    chown -R pixel:pixel /home/pixel
    chmod 700 /home/pixel/.ssh

    systemctl enable --now ssh
{{- if .Egress}}

    # Install nftables separately with noninteractive + confold to keep our
    # pre-written /etc/nftables.conf and avoid dpkg conffile prompts.
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq -o Dpkg::Options::="--force-confold" nftables dnsutils
    /usr/local/bin/pixels-resolve-egress.sh
{{- end}}
    touch /root/.ssh-provisioned
fi
{{- if .DevTools}}
if [ -f /etc/systemd/system/pixels-devtools.service ] && [ ! -f /root/.devtools-provisioned ]; then
    systemctl daemon-reload
    systemctl start pixels-devtools.service
fi
{{- end}}
exit 0
`))

func buildRCLocal(egress, devtools bool) string {
	var b strings.Builder
	if err := rcLocalTmpl.Execute(&b, rcLocalParams{Egress: egress, DevTools: devtools}); err != nil {
		panic(fmt.Sprintf("executing rc.local template: %v", err))
	}
	return b.String()
}

const devtoolsSetupScript = `#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

echo "[$(date -Iseconds)] pixels devtools setup starting"

apt-get update -qq
apt-get install -y -qq build-essential git curl

# Install mise and dev tools for the pixel user (runs as root, uses su -).
su - pixel -c 'curl -fsSL https://mise.run | sh'
su - pixel -c 'echo '\''eval "$(/home/pixel/.local/bin/mise activate bash)"'\'' >> /home/pixel/.bashrc'
su - pixel -c '/home/pixel/.local/bin/mise use --global node@lts'
su - pixel -c '/home/pixel/.local/bin/mise exec -- npm install -g @anthropic-ai/claude-code @openai/codex opencode-ai'

touch /root/.devtools-provisioned
echo "[$(date -Iseconds)] pixels devtools setup complete"
`

const devtoolsServiceUnit = `[Unit]
Description=Pixels dev tools provisioning
After=network-online.target ssh.service
Wants=network-online.target
ConditionPathExists=!/root/.devtools-provisioned

[Service]
Type=oneshot
Environment="HOME=/root"
ExecStart=/usr/local/bin/pixels-setup-devtools.sh
RemainAfterExit=yes
TimeoutStartSec=600
`

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

// ReplaceContainerRootfs destroys the container's ZFS dataset and clones
// the checkpoint snapshot in its place. The container must be stopped.
//
// The pool.dataset.* APIs can't see .ix-virt managed datasets, so we use
// a temporary cron job to run raw ZFS commands on the host as root.
func (c *Client) ReplaceContainerRootfs(ctx context.Context, containerName, snapshotID string) error {
	gcfg, err := c.Virt.GetGlobalConfig(ctx)
	if err != nil {
		return fmt.Errorf("querying virt global config: %w", err)
	}
	if gcfg.Dataset == "" {
		return fmt.Errorf("no dataset in virt global config")
	}
	dstDataset := gcfg.Dataset + "/containers/" + containerName

	// Validate paths contain only safe characters (alphanumeric, .-_/@).
	for _, p := range []string{dstDataset, snapshotID} {
		for _, ch := range p {
			if !isZFSPathChar(ch) {
				return fmt.Errorf("unsafe character %q in ZFS path %q", string(ch), p)
			}
		}
	}

	cmd := fmt.Sprintf(
		"/usr/sbin/zfs destroy -r %s && /usr/sbin/zfs clone %s %s"+
			" && tmp=$(mktemp -d) && mount -t zfs %s \"$tmp\""+
			" && echo '%s' > \"$tmp/rootfs/etc/hostname\""+
			" && umount \"$tmp\" && rmdir \"$tmp\"",
		dstDataset, snapshotID, dstDataset, dstDataset, containerName,
	)

	// Create a disabled cron job — we run it manually, then delete it.
	job, err := c.Cron.Create(ctx, truenas.CreateCronJobOpts{
		Command:     cmd,
		User:        "root",
		Description: "pixels: clone checkpoint (temporary)",
		Enabled:     false,
		Schedule: truenas.Schedule{
			Minute: "00",
			Hour:   "00",
			Dom:    "1",
			Month:  "1",
			Dow:    "1",
		},
	})
	if err != nil {
		return fmt.Errorf("creating temp cron job: %w", err)
	}

	// Always clean up the cron job, even if run fails.
	defer func() {
		_ = c.Cron.Delete(ctx, job.ID)
	}()

	// Run the cron job and wait for completion.
	if err := c.Cron.Run(ctx, job.ID, false); err != nil {
		return fmt.Errorf("running ZFS clone: %w", err)
	}

	return nil
}

// WriteAuthorizedKey writes an SSH public key to a running container's
// authorized_keys files (root and pixel user) via the TrueNAS file_receive API.
func (c *Client) WriteAuthorizedKey(ctx context.Context, name, sshPubKey string) error {
	gcfg, err := c.Virt.GetGlobalConfig(ctx)
	if err != nil {
		return err
	}
	if gcfg.Pool == "" {
		return fmt.Errorf("no pool in virt global config")
	}

	rootfs := fmt.Sprintf("/var/lib/incus/storage-pools/%s/containers/%s/rootfs", gcfg.Pool, name)
	keyData := []byte(sshPubKey + "\n")

	if err := c.Filesystem.WriteFile(ctx, rootfs+"/root/.ssh/authorized_keys", truenas.WriteFileParams{
		Content: keyData,
		Mode:    0o600,
	}); err != nil {
		return fmt.Errorf("writing root authorized_keys: %w", err)
	}

	pixelUID := intPtr(1000)
	if err := c.Filesystem.WriteFile(ctx, rootfs+"/home/pixel/.ssh/authorized_keys", truenas.WriteFileParams{
		Content: keyData,
		Mode:    0o600,
		UID:     pixelUID,
		GID:     pixelUID,
	}); err != nil {
		return fmt.Errorf("writing pixel authorized_keys: %w", err)
	}

	return nil
}

// isZFSPathChar returns true if the rune is valid in a ZFS dataset/snapshot path.
func isZFSPathChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
		r == '/' || r == '-' || r == '_' || r == '.' || r == '@'
}

func intPtr(v int) *int { return &v }
