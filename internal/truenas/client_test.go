package truenas

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	truenas "github.com/deevus/truenas-go"
	"github.com/deevus/truenas-go/client"
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

// gatewayResponse returns JSON for network.general.summary with the given routes.
func gatewayResponse(routes ...string) json.RawMessage {
	v := struct {
		DefaultRoutes []string `json:"default_routes"`
	}{DefaultRoutes: routes}
	b, _ := json.Marshal(v)
	return b
}

func TestDefaultNIC(t *testing.T) {
	tests := []struct {
		name       string
		ifaces     []truenas.NetworkInterface
		ifaceErr   error
		gateway    json.RawMessage
		gatewayErr error
		wantParent string
		wantErr    bool
	}{
		{
			name:       "single interface with gateway match",
			ifaces:     []truenas.NetworkInterface{physicalUp("eno1", "192.168.1.100", 24)},
			gateway:    gatewayResponse("192.168.1.1"),
			wantParent: "eno1",
		},
		{
			name: "gateway matches second interface",
			ifaces: []truenas.NetworkInterface{
				physicalUp("eno1", "192.168.1.100", 24),
				physicalUp("eno2", "10.0.0.50", 24),
			},
			gateway:    gatewayResponse("10.0.0.1"),
			wantParent: "eno2",
		},
		{
			name:       "no gateway falls back to first",
			ifaces:     []truenas.NetworkInterface{physicalUp("eno1", "192.168.1.100", 24)},
			gatewayErr: errors.New("api error"),
			wantParent: "eno1",
		},
		{
			name: "gateway outside all subnets falls back to first",
			ifaces: []truenas.NetworkInterface{
				physicalUp("eno1", "192.168.1.100", 24),
				physicalUp("eno2", "10.0.0.50", 24),
			},
			gateway:    gatewayResponse("172.16.0.1"),
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
			gateway:    gatewayResponse("fe80::1"),
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
				ws: &client.MockClient{
					CallFunc: func(ctx context.Context, method string, params any) (json.RawMessage, error) {
						if method == "network.general.summary" {
							return tt.gateway, tt.gatewayErr
						}
						return nil, nil
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
		resp    string
		wantDS  string
		wantErr bool
	}{
		{
			name:   "returns dataset path",
			resp:   `{"pool":"tank","dataset":"tank/ix-virt","storage_pools":["tank"]}`,
			wantDS: "tank/ix-virt/containers/px-test",
		},
		{
			name:    "empty dataset",
			resp:    `{"pool":"tank","dataset":"","storage_pools":["tank"]}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Client{
				ws: &client.MockClient{
					CallFunc: func(ctx context.Context, method string, params any) (json.RawMessage, error) {
						if method == "virt.global.config" {
							return json.RawMessage(tt.resp), nil
						}
						return nil, nil
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

func TestListSnapshots_VersionDispatch(t *testing.T) {
	tests := []struct {
		name       string
		version    truenas.Version
		wantMethod string
	}{
		{
			name:       "pre-25.10 uses zfs.snapshot.query",
			version:    truenas.Version{Major: 25, Minor: 4},
			wantMethod: "zfs.snapshot.query",
		},
		{
			name:       "25.10+ uses pool.snapshot.query",
			version:    truenas.Version{Major: 25, Minor: 10},
			wantMethod: "pool.snapshot.query",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calledMethod string
			c := &Client{
				version: tt.version,
				ws: &client.MockClient{
					CallFunc: func(ctx context.Context, method string, params any) (json.RawMessage, error) {
						calledMethod = method
						return json.RawMessage(`[]`), nil
					},
				},
			}

			_, err := c.ListSnapshots(context.Background(), "tank/containers/px-test")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if calledMethod != tt.wantMethod {
				t.Errorf("called %q, want %q", calledMethod, tt.wantMethod)
			}
		})
	}
}
