package truenas

import (
	"context"
	"errors"
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
