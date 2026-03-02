package truenas

import (
	"context"
	"strings"
	"testing"

	tnapi "github.com/deevus/truenas-go"

	"github.com/deevus/pixels/internal/cache"
)

func TestResolveRunningIP(t *testing.T) {
	tests := []struct {
		name      string
		cached    *cache.Entry
		instance  *tnapi.VirtInstance
		getErr    error
		wantIP    string
		wantErr   string
	}{
		{
			name:   "cache hit",
			cached: &cache.Entry{IP: "10.0.0.5", Status: "RUNNING"},
			wantIP: "10.0.0.5",
		},
		{
			name:   "cache miss, no IP cached",
			cached: &cache.Entry{IP: "", Status: "RUNNING"},
			instance: &tnapi.VirtInstance{
				Name:   "px-test",
				Status: "RUNNING",
				Aliases: []tnapi.VirtAlias{
					{Type: "INET", Address: "10.0.0.10"},
				},
			},
			wantIP: "10.0.0.10",
		},
		{
			name:   "cache miss, status not running",
			cached: &cache.Entry{IP: "10.0.0.5", Status: "STOPPED"},
			instance: &tnapi.VirtInstance{
				Name:   "px-test",
				Status: "RUNNING",
				Aliases: []tnapi.VirtAlias{
					{Type: "INET", Address: "10.0.0.10"},
				},
			},
			wantIP: "10.0.0.10",
		},
		{
			name: "no cache, API lookup",
			instance: &tnapi.VirtInstance{
				Name:   "px-test",
				Status: "RUNNING",
				Aliases: []tnapi.VirtAlias{
					{Type: "INET", Address: "192.168.1.50"},
				},
			},
			wantIP: "192.168.1.50",
		},
		{
			name:    "API error",
			getErr:  context.DeadlineExceeded,
			wantErr: "looking up test",
		},
		{
			name:    "instance not found",
			wantErr: "not found",
		},
		{
			name: "instance not running",
			instance: &tnapi.VirtInstance{
				Name:   "px-test",
				Status: "STOPPED",
			},
			wantErr: "is STOPPED",
		},
		{
			name: "no IP address",
			instance: &tnapi.VirtInstance{
				Name:   "px-test",
				Status: "RUNNING",
			},
			wantErr: "no IP address",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clean up cache.
			cache.Delete("test")
			if tt.cached != nil {
				cache.Put("test", tt.cached)
			}
			defer cache.Delete("test")

			tn := &TrueNAS{
				client: &Client{
					Virt: &tnapi.MockVirtService{
						GetInstanceFunc: func(ctx context.Context, name string) (*tnapi.VirtInstance, error) {
							if name != "px-test" {
								t.Errorf("GetInstance called with %q, want px-test", name)
							}
							if tt.getErr != nil {
								return nil, tt.getErr
							}
							return tt.instance, nil
						},
					},
				},
			}

			ip, err := tn.resolveRunningIP(context.Background(), "test")
			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ip != tt.wantIP {
				t.Errorf("ip = %q, want %q", ip, tt.wantIP)
			}
		})
	}
}

func TestIPFromAliases(t *testing.T) {
	tests := []struct {
		name    string
		aliases []tnapi.VirtAlias
		want    string
	}{
		{
			name: "INET type",
			aliases: []tnapi.VirtAlias{
				{Type: "INET", Address: "10.0.0.1"},
			},
			want: "10.0.0.1",
		},
		{
			name: "ipv4 type",
			aliases: []tnapi.VirtAlias{
				{Type: "ipv4", Address: "192.168.1.1"},
			},
			want: "192.168.1.1",
		},
		{
			name: "skips INET6",
			aliases: []tnapi.VirtAlias{
				{Type: "INET6", Address: "fe80::1"},
				{Type: "INET", Address: "10.0.0.1"},
			},
			want: "10.0.0.1",
		},
		{
			name:    "no aliases",
			aliases: nil,
			want:    "",
		},
		{
			name: "only IPv6",
			aliases: []tnapi.VirtAlias{
				{Type: "INET6", Address: "fe80::1"},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ipFromAliases(tt.aliases)
			if got != tt.want {
				t.Errorf("ipFromAliases = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrefixed(t *testing.T) {
	if got := prefixed("mybox"); got != "px-mybox" {
		t.Errorf("prefixed(mybox) = %q", got)
	}
}

func TestUnprefixed(t *testing.T) {
	if got := unprefixed("px-mybox"); got != "mybox" {
		t.Errorf("unprefixed(px-mybox) = %q", got)
	}
	if got := unprefixed("mybox"); got != "mybox" {
		t.Errorf("unprefixed(mybox) = %q", got)
	}
}
