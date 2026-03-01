package truenas

import (
	"context"
	"errors"
	"strings"
	"testing"

	truenas "github.com/deevus/truenas-go"
)

// physicalUp returns a physical, UP interface with the given name and IPv4 alias.
func physicalUp(name, addr string, mask int) truenas.NetworkInterface {
	return truenas.NetworkInterface{
		ID:   name,
		Name: name,
		Type: truenas.InterfaceTypePhysical,
		State: truenas.InterfaceState{
			LinkState: truenas.LinkStateUp,
		},
		Aliases: []truenas.InterfaceAlias{
			{Type: truenas.AliasTypeINET, Address: addr, Netmask: mask},
		},
	}
}

func TestDefaultNIC(t *testing.T) {
	tests := []struct {
		name       string
		ifaces     []truenas.NetworkInterface
		ifaceErr   error
		routes     []string
		networkErr error
		wantParent string
		wantErr    bool
	}{
		{
			name:       "single interface with gateway match",
			ifaces:     []truenas.NetworkInterface{physicalUp("eno1", "192.168.1.100", 24)},
			routes:     []string{"192.168.1.1"},
			wantParent: "eno1",
		},
		{
			name: "gateway matches second interface",
			ifaces: []truenas.NetworkInterface{
				physicalUp("eno1", "192.168.1.100", 24),
				physicalUp("eno2", "10.0.0.50", 24),
			},
			routes:     []string{"10.0.0.1"},
			wantParent: "eno2",
		},
		{
			name:       "no gateway falls back to first",
			ifaces:     []truenas.NetworkInterface{physicalUp("eno1", "192.168.1.100", 24)},
			networkErr: errors.New("api error"),
			wantParent: "eno1",
		},
		{
			name: "gateway outside all subnets falls back to first",
			ifaces: []truenas.NetworkInterface{
				physicalUp("eno1", "192.168.1.100", 24),
				physicalUp("eno2", "10.0.0.50", 24),
			},
			routes:     []string{"172.16.0.1"},
			wantParent: "eno1",
		},
		{
			name:    "no physical interfaces",
			ifaces:  []truenas.NetworkInterface{},
			wantErr: true,
		},
		{
			name: "only bridge interfaces",
			ifaces: []truenas.NetworkInterface{
				{
					Name: "br0", Type: truenas.InterfaceTypeBridge,
					State:   truenas.InterfaceState{LinkState: truenas.LinkStateUp},
					Aliases: []truenas.InterfaceAlias{{Type: truenas.AliasTypeINET, Address: "10.0.0.1", Netmask: 24}},
				},
			},
			wantErr: true,
		},
		{
			name: "physical but down",
			ifaces: []truenas.NetworkInterface{
				{
					Name: "eno1", Type: truenas.InterfaceTypePhysical,
					State: truenas.InterfaceState{LinkState: truenas.LinkStateDown},
					Aliases: []truenas.InterfaceAlias{{Type: truenas.AliasTypeINET, Address: "10.0.0.1", Netmask: 24}},
				},
			},
			wantErr: true,
		},
		{
			name: "physical up but only IPv6",
			ifaces: []truenas.NetworkInterface{
				{
					Name: "eno1", Type: truenas.InterfaceTypePhysical,
					State:   truenas.InterfaceState{LinkState: truenas.LinkStateUp},
					Aliases: []truenas.InterfaceAlias{{Type: truenas.AliasTypeINET6, Address: "fe80::1", Netmask: 64}},
				},
			},
			wantErr: true,
		},
		{
			name:     "interface list error",
			ifaceErr: errors.New("connection refused"),
			wantErr:  true,
		},
		{
			name: "ipv6 gateway ignored, falls back to first",
			ifaces: []truenas.NetworkInterface{
				physicalUp("eno1", "192.168.1.100", 24),
			},
			routes:     []string{"fe80::1"},
			wantParent: "eno1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Client{
				Interface: &truenas.MockInterfaceService{
					ListFunc: func(ctx context.Context) ([]truenas.NetworkInterface, error) {
						return tt.ifaces, tt.ifaceErr
					},
				},
				Network: &truenas.MockNetworkService{
					GetSummaryFunc: func(ctx context.Context) (*truenas.NetworkSummary, error) {
						if tt.networkErr != nil {
							return nil, tt.networkErr
						}
						return &truenas.NetworkSummary{DefaultRoutes: tt.routes}, nil
					},
				},
			}

			nic, err := c.DefaultNIC(context.Background())
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if nic.NICType != "MACVLAN" {
				t.Errorf("NICType = %q, want MACVLAN", nic.NICType)
			}
			if nic.Parent != tt.wantParent {
				t.Errorf("Parent = %q, want %q", nic.Parent, tt.wantParent)
			}
		})
	}
}

func TestContainerDataset(t *testing.T) {
	tests := []struct {
		name    string
		dataset string
		pool    string
		wantDS  string
		wantErr bool
	}{
		{
			name:    "returns dataset path",
			dataset: "tank/ix-virt",
			pool:    "tank",
			wantDS:  "tank/ix-virt/containers/px-test",
		},
		{
			name:    "empty dataset",
			dataset: "",
			pool:    "tank",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Client{
				Virt: &truenas.MockVirtService{
					GetGlobalConfigFunc: func(ctx context.Context) (*truenas.VirtGlobalConfig, error) {
						return &truenas.VirtGlobalConfig{
							Dataset: tt.dataset,
							Pool:    tt.pool,
						}, nil
					},
				},
			}

			ds, err := c.ContainerDataset(context.Background(), "px-test")
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ds != tt.wantDS {
				t.Errorf("dataset = %q, want %q", ds, tt.wantDS)
			}
		})
	}
}

type writeCall struct {
	path    string
	content string
	mode    uint32
	uid     *int
	gid     *int
}

func TestProvision(t *testing.T) {
	tests := []struct {
		name       string
		opts       ProvisionOpts
		pool       string
		configErr  error
		writeErr   error
		wantCalls  int
		wantErr    bool
		wantErrMsg string
		check      func(t *testing.T, calls []writeCall)
	}{
		{
			name: "full provisioning with ssh, dns, env, devtools",
			opts: ProvisionOpts{
				SSHPubKey: "ssh-ed25519 AAAA test@host",
				DNS:       []string{"1.1.1.1", "8.8.8.8"},
				Env:       map[string]string{"ANTHROPIC_API_KEY": "sk-ant-123"},
				DevTools:  true,
			},
			pool:      "tank",
			wantCalls: 7, // dns + sshd config + env + root key + pixel key + setup script + rc.local
			check: func(t *testing.T, calls []writeCall) {
				paths := make(map[string]writeCall)
				for _, c := range calls {
					paths[c.path] = c
				}
				rootfs := "/var/lib/incus/storage-pools/tank/containers/px-test/rootfs"

				// /etc/environment.
				env := paths[rootfs+"/etc/environment"]
				if !strings.Contains(env.content, "ANTHROPIC_API_KEY=") {
					t.Errorf("/etc/environment missing API key: %s", env.content)
				}

				// Setup script.
				setup := paths[rootfs+"/usr/local/bin/pixels-setup-devtools.sh"]
				if setup.mode != 0o755 {
					t.Errorf("setup script mode = %o, want 755", setup.mode)
				}
				for _, want := range []string{"mise", "npm install", "claude-code", "opencode", "su - pixel"} {
					if !strings.Contains(setup.content, want) {
						t.Errorf("setup script missing %q", want)
					}
				}

				// No systemd unit should be written.
				if _, ok := paths[rootfs+"/etc/systemd/system/pixels-devtools.service"]; ok {
					t.Error("systemd unit should not be written (zmx handles devtools)")
				}

				// rc.local should have SSH bootstrap only, no devtools.
				rc := paths[rootfs+"/etc/rc.local"]
				if strings.Contains(rc.content, "pixels-devtools") {
					t.Error("rc.local should not reference devtools")
				}
				for _, want := range []string{"set -e", "useradd -m -u 1000 -g 1000 -s /bin/bash -G sudo pixel", "NOPASSWD:ALL"} {
					if !strings.Contains(rc.content, want) {
						t.Errorf("rc.local missing %q", want)
					}
				}
			},
		},
		{
			name: "ssh key and dns",
			opts: ProvisionOpts{
				SSHPubKey: "ssh-ed25519 AAAA test@host",
				DNS:       []string{"1.1.1.1", "8.8.8.8"},
			},
			pool:      "tank",
			wantCalls: 5, // sshd config + dns + root key + pixel key + rc.local
			check: func(t *testing.T, calls []writeCall) {
				// No devtools files should be written.
				for _, c := range calls {
					if strings.Contains(c.path, "pixels-setup-devtools") || strings.Contains(c.path, "pixels-devtools.service") {
						t.Errorf("unexpected devtools file written: %s", c.path)
					}
				}
				// rc.local should NOT contain devtools service start.
				for _, c := range calls {
					if strings.Contains(c.path, "rc.local") && strings.Contains(c.content, "pixels-devtools.service") {
						t.Error("rc.local should not reference devtools service when devtools disabled")
					}
				}
			},
		},
		{
			name: "ssh key only",
			opts: ProvisionOpts{
				SSHPubKey: "ssh-ed25519 AAAA test@host",
			},
			pool:      "tank",
			wantCalls: 4, // sshd config + root key + pixel key + rc.local
		},
		{
			name: "env only, no ssh key",
			opts: ProvisionOpts{
				Env: map[string]string{"FOO": "bar"},
			},
			pool:      "tank",
			wantCalls: 2, // sshd config + /etc/environment
			check: func(t *testing.T, calls []writeCall) {
				if !strings.Contains(calls[1].path, "/etc/environment") {
					t.Errorf("expected /etc/environment, got %s", calls[1].path)
				}
			},
		},
		{
			name: "no ssh key with dns",
			opts: ProvisionOpts{
				DNS: []string{"1.1.1.1"},
			},
			pool:      "tank",
			wantCalls: 2, // dns + sshd config
		},
		{
			name:      "no ssh key no dns",
			opts:      ProvisionOpts{},
			pool:      "tank",
			wantCalls: 1, // sshd config only
		},
		{
			name:      "global config error",
			opts:      ProvisionOpts{SSHPubKey: "ssh-ed25519 AAAA"},
			configErr: errors.New("api failure"),
			wantErr:   true,
		},
		{
			name:       "empty pool",
			opts:       ProvisionOpts{SSHPubKey: "ssh-ed25519 AAAA"},
			pool:       "",
			wantErr:    true,
			wantErrMsg: "no pool",
		},
		{
			name:     "write error",
			opts:     ProvisionOpts{SSHPubKey: "ssh-ed25519 AAAA"},
			pool:     "tank",
			writeErr: errors.New("disk full"),
			wantErr:  true,
		},
		{
			name: "egress agent provisioning",
			opts: ProvisionOpts{
				SSHPubKey: "ssh-ed25519 AAAA test@host",
				Egress:    "agent",
			},
			pool:      "tank",
			wantCalls: 12, // sshd config + root key + pixel key + domains + cidrs + nftables.conf + resolve script + safe-apt + sudoers.restricted + setup-egress + enable-egress + rc.local
			check: func(t *testing.T, calls []writeCall) {
				paths := make(map[string]writeCall)
				for _, c := range calls {
					paths[c.path] = c
				}
				rootfs := "/var/lib/incus/storage-pools/tank/containers/px-test/rootfs"

				// Egress domains file.
				domains := paths[rootfs+"/etc/pixels-egress-domains"]
				if !strings.Contains(domains.content, "api.anthropic.com") {
					t.Error("domains file missing api.anthropic.com")
				}

				// nftables.conf.
				nft := paths[rootfs+"/etc/nftables.conf"]
				if !strings.Contains(nft.content, "pixels_egress") {
					t.Error("nftables.conf missing pixels_egress table")
				}

				// Resolve script.
				script := paths[rootfs+"/usr/local/bin/pixels-resolve-egress.sh"]
				if script.mode != 0o755 {
					t.Errorf("resolve script mode = %o, want 755", script.mode)
				}

				// Egress setup and enable scripts.
				setup := paths[rootfs+"/usr/local/bin/pixels-setup-egress.sh"]
				if setup.mode != 0o755 {
					t.Errorf("egress setup script mode = %o, want 755", setup.mode)
				}
				if !strings.Contains(setup.content, "nftables") {
					t.Error("egress setup script missing nftables install")
				}
				enable := paths[rootfs+"/usr/local/bin/pixels-enable-egress.sh"]
				if enable.mode != 0o755 {
					t.Errorf("egress enable script mode = %o, want 755", enable.mode)
				}
				if !strings.Contains(enable.content, "pixels-resolve-egress.sh") {
					t.Error("egress enable script missing resolve call")
				}

				// Restricted sudoers staged at .restricted path.
				sudoers := paths[rootfs+"/etc/sudoers.d/pixel.restricted"]
				if strings.Contains(sudoers.content, "NOPASSWD: ALL") {
					t.Error("sudoers should be restricted, not blanket ALL")
				}
				if !strings.Contains(sudoers.content, "/usr/local/bin/safe-apt") {
					t.Error("sudoers missing safe-apt allowlist")
				}

				// rc.local should be SSH-only bootstrap (no egress setup).
				rc := paths[rootfs+"/etc/rc.local"]
				if strings.Contains(rc.content, "nftables") {
					t.Error("rc.local should not contain nftables (zmx handles egress)")
				}
				// rc.local always writes unrestricted sudoers; zmx egress step replaces it.
				if !strings.Contains(rc.content, "NOPASSWD:ALL") {
					t.Error("rc.local should have unrestricted sudoers")
				}
			},
		},
		{
			name: "provision script written when provided",
			opts: ProvisionOpts{
				SSHPubKey:       "ssh-ed25519 AAAA test@host",
				ProvisionScript: "#!/bin/sh\necho hello\n",
			},
			pool:      "tank",
			wantCalls: 5, // sshd config + root key + pixel key + provision script + rc.local
			check: func(t *testing.T, calls []writeCall) {
				paths := make(map[string]writeCall)
				for _, c := range calls {
					paths[c.path] = c
				}
				rootfs := "/var/lib/incus/storage-pools/tank/containers/px-test/rootfs"

				ps := paths[rootfs+"/usr/local/bin/pixels-provision.sh"]
				if ps.path == "" {
					t.Fatal("provision script not written")
				}
				if ps.mode != 0o755 {
					t.Errorf("provision script mode = %o, want 755", ps.mode)
				}
				if !strings.Contains(ps.content, "echo hello") {
					t.Error("provision script missing content")
				}

				// rc.local should include nohup launch.
				rc := paths[rootfs+"/etc/rc.local"]
				if !strings.Contains(rc.content, "pixels-provision.sh") {
					t.Error("rc.local missing provision script launch")
				}
			},
		},
		{
			name: "egress unrestricted skips egress files",
			opts: ProvisionOpts{
				SSHPubKey: "ssh-ed25519 AAAA test@host",
				Egress:    "unrestricted",
			},
			pool:      "tank",
			wantCalls: 4, // sshd config + root key + pixel key + rc.local (no egress files)
			check: func(t *testing.T, calls []writeCall) {
				for _, c := range calls {
					if strings.Contains(c.path, "pixels-egress") || strings.Contains(c.path, "nftables") {
						t.Errorf("unexpected egress file in unrestricted mode: %s", c.path)
					}
				}
			},
		},
		{
			name: "egress allowlist with custom domains",
			opts: ProvisionOpts{
				SSHPubKey:   "ssh-ed25519 AAAA test@host",
				Egress:      "allowlist",
				EgressAllow: []string{"custom.example.com"},
			},
			pool:      "tank",
			wantCalls: 11, // sshd config + root key + pixel key + domains + nftables.conf + resolve script + safe-apt + sudoers + setup-egress + enable-egress + rc.local
			check: func(t *testing.T, calls []writeCall) {
				rootfs := "/var/lib/incus/storage-pools/tank/containers/px-test/rootfs"
				for _, c := range calls {
					if c.path == rootfs+"/etc/pixels-egress-domains" {
						if !strings.Contains(c.content, "custom.example.com") {
							t.Error("domains file missing custom domain")
						}
						if strings.Contains(c.content, "api.anthropic.com") {
							t.Error("allowlist mode should not include agent preset domains")
						}
						return
					}
				}
				t.Error("domains file not written")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []writeCall

			c := &Client{
				Virt: &truenas.MockVirtService{
					GetGlobalConfigFunc: func(ctx context.Context) (*truenas.VirtGlobalConfig, error) {
						if tt.configErr != nil {
							return nil, tt.configErr
						}
						return &truenas.VirtGlobalConfig{Pool: tt.pool}, nil
					},
				},
				Filesystem: &truenas.MockFilesystemService{
					WriteFileFunc: func(ctx context.Context, path string, params truenas.WriteFileParams) error {
						if tt.writeErr != nil {
							return tt.writeErr
						}
						calls = append(calls, writeCall{
							path:    path,
							content: string(params.Content),
							mode:    uint32(params.Mode),
							uid:     params.UID,
							gid:     params.GID,
						})
						return nil
					},
				},
			}

			err := c.Provision(context.Background(), "px-test", tt.opts)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.wantErrMsg != "" && !strings.Contains(err.Error(), tt.wantErrMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErrMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(calls) != tt.wantCalls {
				for i, c := range calls {
					t.Logf("  call[%d]: %s", i, c.path)
				}
				t.Fatalf("got %d WriteFile calls, want %d", len(calls), tt.wantCalls)
			}

			if tt.check != nil {
				tt.check(t, calls)
				return
			}

			if tt.wantCalls == 0 {
				return
			}

			rootfs := "/var/lib/incus/storage-pools/tank/containers/px-test/rootfs"

			// Check DNS drop-in if DNS was provided.
			idx := 0
			if len(tt.opts.DNS) > 0 {
				dns := calls[idx]
				if dns.path != rootfs+"/etc/systemd/resolved.conf.d/pixels-dns.conf" {
					t.Errorf("dns path = %q, want resolved drop-in", dns.path)
				}
				if !strings.Contains(dns.content, "1.1.1.1") {
					t.Error("dns content missing nameserver")
				}
				idx++
			}

			// Check sshd AcceptEnv drop-in (always written).
			sshd := calls[idx]
			if sshd.path != rootfs+"/etc/ssh/sshd_config.d/pixels.conf" {
				t.Errorf("sshd config path = %q, want sshd drop-in", sshd.path)
			}
			if !strings.Contains(sshd.content, "AcceptEnv *") {
				t.Error("sshd config missing AcceptEnv *")
			}
			idx++

			if tt.opts.SSHPubKey == "" {
				return
			}

			// Check authorized_keys (root).
			ak := calls[idx]
			if ak.path != rootfs+"/root/.ssh/authorized_keys" {
				t.Errorf("authorized_keys path = %q", ak.path)
			}
			if !strings.Contains(ak.content, tt.opts.SSHPubKey) {
				t.Error("authorized_keys missing public key")
			}
			if ak.mode != 0o600 {
				t.Errorf("authorized_keys mode = %o, want 600", ak.mode)
			}
			idx++

			// Check authorized_keys (pixel) — should have UID/GID 1000.
			akPixel := calls[idx]
			if akPixel.path != rootfs+"/home/pixel/.ssh/authorized_keys" {
				t.Errorf("pixel authorized_keys path = %q", akPixel.path)
			}
			if akPixel.uid == nil || *akPixel.uid != 1000 {
				t.Errorf("pixel authorized_keys UID = %v, want 1000", akPixel.uid)
			}
			if akPixel.gid == nil || *akPixel.gid != 1000 {
				t.Errorf("pixel authorized_keys GID = %v, want 1000", akPixel.gid)
			}
			idx++

			// Check rc.local.
			rc := calls[idx]
			if rc.path != rootfs+"/etc/rc.local" {
				t.Errorf("rc.local path = %q", rc.path)
			}
			if rc.mode != 0o755 {
				t.Errorf("rc.local mode = %o, want 755", rc.mode)
			}

			// Verify rc.local provisions the pixel user and launches provision script.
			for _, want := range []string{
				"set -e",
				"openssh-server sudo curl",
				"useradd -m -u 1000 -g 1000 -s /bin/bash -G sudo pixel",
				"NOPASSWD:ALL",
				"/home/pixel/.ssh",
				"chown -R pixel:pixel",
				"pixels-provision.sh",
				"nohup",
			} {
				if !strings.Contains(rc.content, want) {
					t.Errorf("rc.local missing %q", want)
				}
			}
		})
	}
}

func TestCreateInstance(t *testing.T) {
	var captured truenas.CreateVirtInstanceOpts

	c := &Client{
		Virt: &truenas.MockVirtService{
			CreateInstanceFunc: func(ctx context.Context, opts truenas.CreateVirtInstanceOpts) (*truenas.VirtInstance, error) {
				captured = opts
				return &truenas.VirtInstance{Name: opts.Name}, nil
			},
		},
	}

	t.Run("with NIC", func(t *testing.T) {
		inst, err := c.CreateInstance(context.Background(), CreateInstanceOpts{
			Name: "px-test", Image: "ubuntu/24.04", CPU: "2", Memory: 2048,
			Autostart: true, NIC: &NICOpts{NICType: "MACVLAN", Parent: "eno1"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if inst.Name != "px-test" {
			t.Errorf("name = %q, want px-test", inst.Name)
		}
		if captured.InstanceType != "CONTAINER" {
			t.Errorf("instance type = %q, want CONTAINER", captured.InstanceType)
		}
		if len(captured.Devices) != 1 || captured.Devices[0].DevType != "NIC" {
			t.Errorf("expected 1 NIC device, got %v", captured.Devices)
		}
	})

	t.Run("without NIC", func(t *testing.T) {
		_, err := c.CreateInstance(context.Background(), CreateInstanceOpts{
			Name: "px-bare", Image: "ubuntu/24.04",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(captured.Devices) != 0 {
			t.Errorf("expected no devices, got %v", captured.Devices)
		}
	})
}

func TestListInstances(t *testing.T) {
	var calledFilters [][]any
	c := &Client{
		Virt: &truenas.MockVirtService{
			ListInstancesFunc: func(ctx context.Context, filters [][]any) ([]truenas.VirtInstance, error) {
				calledFilters = filters
				return []truenas.VirtInstance{{Name: "px-one"}}, nil
			},
		},
	}

	instances, err := c.ListInstances(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(instances))
	}
	if len(calledFilters) != 1 || calledFilters[0][0] != "name" || calledFilters[0][1] != "^" || calledFilters[0][2] != "px-" {
		t.Errorf("unexpected filters: %v", calledFilters)
	}
}

func TestSnapshotRollback(t *testing.T) {
	var calledID string
	c := &Client{
		Snapshot: &truenas.MockSnapshotService{
			RollbackFunc: func(ctx context.Context, id string) error {
				calledID = id
				return nil
			},
		},
	}

	err := c.SnapshotRollback(context.Background(), "tank/containers/px-test@snap1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calledID != "tank/containers/px-test@snap1" {
		t.Errorf("rollback id = %q", calledID)
	}
}

func TestListSnapshots(t *testing.T) {
	var calledFilters [][]any
	c := &Client{
		Snapshot: &truenas.MockSnapshotService{
			QueryFunc: func(ctx context.Context, filters [][]any) ([]truenas.Snapshot, error) {
				calledFilters = filters
				return []truenas.Snapshot{}, nil
			},
		},
	}

	_, err := c.ListSnapshots(context.Background(), "tank/containers/px-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calledFilters) != 1 {
		t.Fatalf("expected 1 filter, got %d", len(calledFilters))
	}
	if calledFilters[0][0] != "dataset" || calledFilters[0][1] != "=" || calledFilters[0][2] != "tank/containers/px-test" {
		t.Errorf("unexpected filter: %v", calledFilters[0])
	}
}

func TestWriteAuthorizedKey(t *testing.T) {
	tests := []struct {
		name       string
		pool       string
		pubKey     string
		configErr  error
		writeErr   error
		wantErr    bool
		wantErrMsg string
		wantCalls  int
		check      func(t *testing.T, calls []writeCall)
	}{
		{
			name:      "writes key to both root and pixel authorized_keys",
			pool:      "tank",
			pubKey:    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAItest user@newmachine",
			wantCalls: 2,
			check: func(t *testing.T, calls []writeCall) {
				rootfs := "/var/lib/incus/storage-pools/tank/containers/px-test/rootfs"

				root := calls[0]
				if root.path != rootfs+"/root/.ssh/authorized_keys" {
					t.Errorf("root path = %q", root.path)
				}
				if !strings.Contains(root.content, "ssh-ed25519") {
					t.Error("root authorized_keys missing key")
				}
				if root.mode != 0o600 {
					t.Errorf("root mode = %o, want 600", root.mode)
				}

				pixel := calls[1]
				if pixel.path != rootfs+"/home/pixel/.ssh/authorized_keys" {
					t.Errorf("pixel path = %q", pixel.path)
				}
				if pixel.uid == nil || *pixel.uid != 1000 {
					t.Errorf("pixel UID = %v, want 1000", pixel.uid)
				}
				if pixel.gid == nil || *pixel.gid != 1000 {
					t.Errorf("pixel GID = %v, want 1000", pixel.gid)
				}
			},
		},
		{
			name:      "global config error",
			pubKey:    "ssh-ed25519 AAAA test@host",
			configErr: errors.New("api failure"),
			wantErr:   true,
		},
		{
			name:       "empty pool",
			pool:       "",
			pubKey:     "ssh-ed25519 AAAA test@host",
			wantErr:    true,
			wantErrMsg: "no pool",
		},
		{
			name:     "write error",
			pool:     "tank",
			pubKey:   "ssh-ed25519 AAAA test@host",
			writeErr: errors.New("disk full"),
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []writeCall

			c := &Client{
				Virt: &truenas.MockVirtService{
					GetGlobalConfigFunc: func(ctx context.Context) (*truenas.VirtGlobalConfig, error) {
						if tt.configErr != nil {
							return nil, tt.configErr
						}
						return &truenas.VirtGlobalConfig{Pool: tt.pool}, nil
					},
				},
				Filesystem: &truenas.MockFilesystemService{
					WriteFileFunc: func(ctx context.Context, path string, params truenas.WriteFileParams) error {
						if tt.writeErr != nil {
							return tt.writeErr
						}
						calls = append(calls, writeCall{
							path:    path,
							content: string(params.Content),
							mode:    uint32(params.Mode),
							uid:     params.UID,
							gid:     params.GID,
						})
						return nil
					},
				},
			}

			err := c.WriteAuthorizedKey(context.Background(), "px-test", tt.pubKey)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.wantErrMsg != "" && !strings.Contains(err.Error(), tt.wantErrMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErrMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(calls) != tt.wantCalls {
				t.Fatalf("got %d WriteFile calls, want %d", len(calls), tt.wantCalls)
			}

			if tt.check != nil {
				tt.check(t, calls)
			}
		})
	}
}
