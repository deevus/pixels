package truenas

import (
	"context"
	"strings"
	"testing"

	tnapi "github.com/deevus/truenas-go"
)

func TestEnsureRunning(t *testing.T) {
	tests := []struct {
		name     string
		instance *tnapi.VirtInstance
		getErr   error
		wantErr  string
	}{
		{
			name: "running with IP",
			instance: &tnapi.VirtInstance{
				Name:   "px-test",
				Status: "RUNNING",
				Aliases: []tnapi.VirtAlias{
					{Type: "INET", Address: "192.168.1.50"},
				},
			},
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

			inst, err := tn.ensureRunning(context.Background(), "test")
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
			if inst == nil {
				t.Fatal("expected non-nil instance")
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
