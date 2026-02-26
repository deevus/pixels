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

func TestProvision(t *testing.T) {
	type writeCall struct {
		path    string
		content string
		mode    uint32
	}

	tests := []struct {
		name       string
		opts       ProvisionOpts
		pool       string
		configErr  error
		writeErr   error
		wantCalls  int
		wantErr    bool
		wantErrMsg string
	}{
		{
			name: "ssh key and dns",
			opts: ProvisionOpts{
				SSHPubKey: "ssh-ed25519 AAAA test@host",
				DNS:       []string{"1.1.1.1", "8.8.8.8"},
			},
			pool:      "tank",
			wantCalls: 3, // dns + authorized_keys + rc.local
		},
		{
			name: "ssh key only",
			opts: ProvisionOpts{
				SSHPubKey: "ssh-ed25519 AAAA test@host",
			},
			pool:      "tank",
			wantCalls: 2, // authorized_keys + rc.local
		},
		{
			name: "no ssh key with dns",
			opts: ProvisionOpts{
				DNS: []string{"1.1.1.1"},
			},
			pool:      "tank",
			wantCalls: 1, // dns only
		},
		{
			name:      "no ssh key no dns",
			opts:      ProvisionOpts{},
			pool:      "tank",
			wantCalls: 0,
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
				t.Fatalf("got %d WriteFile calls, want %d", len(calls), tt.wantCalls)
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

			if tt.opts.SSHPubKey == "" {
				return
			}

			// Check authorized_keys.
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

			// Check rc.local.
			rc := calls[idx]
			if rc.path != rootfs+"/etc/rc.local" {
				t.Errorf("rc.local path = %q", rc.path)
			}
			if rc.mode != 0o755 {
				t.Errorf("rc.local mode = %o, want 755", rc.mode)
			}

			// Verify rc.local provisions the pixel user.
			for _, want := range []string{
				"set -e",
				"openssh-server sudo",
				"useradd -m -s /bin/bash -G sudo pixel",
				"NOPASSWD:ALL",
				"/home/pixel/.ssh",
				"chown -R pixel:pixel",
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
