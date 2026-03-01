package cmd

import (
	"context"
	"fmt"
	"strings"
	"testing"

	truenas "github.com/deevus/truenas-go"
)

func TestValidSessionName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"console", "console", true},
		{"build", "build", true},
		{"my-session", "my-session", true},
		{"test.1", "test.1", true},
		{"a_b", "a_b", true},
		{"empty", "", false},
		{"has space", "has space", false},
		{"semicolon", "semi;colon", false},
		{"backtick", "back`tick", false},
		{"newline", "new\nline", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validSessionName.MatchString(tt.input); got != tt.want {
				t.Errorf("validSessionName.MatchString(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestContainerName(t *testing.T) {
	if got := containerName("my-project"); got != "px-my-project" {
		t.Errorf("containerName(my-project) = %q, want %q", got, "px-my-project")
	}
}

func TestDisplayName(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"px-my-project", "my-project"},
		{"px-sandbox", "sandbox"},
		{"no-prefix", "no-prefix"},
	}
	for _, tt := range tests {
		if got := displayName(tt.input); got != tt.want {
			t.Errorf("displayName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestResolveIP(t *testing.T) {
	tests := []struct {
		name     string
		instance *truenas.VirtInstance
		want     string
	}{
		{
			name:     "no aliases",
			instance: &truenas.VirtInstance{},
			want:     "",
		},
		{
			name: "INET alias",
			instance: &truenas.VirtInstance{
				Aliases: []truenas.VirtAlias{
					{Type: "INET", Address: "10.0.0.1"},
				},
			},
			want: "10.0.0.1",
		},
		{
			name: "ipv4 alias",
			instance: &truenas.VirtInstance{
				Aliases: []truenas.VirtAlias{
					{Type: "ipv4", Address: "192.168.1.5"},
				},
			},
			want: "192.168.1.5",
		},
		{
			name: "skips ipv6",
			instance: &truenas.VirtInstance{
				Aliases: []truenas.VirtAlias{
					{Type: "INET6", Address: "::1"},
					{Type: "INET", Address: "10.0.0.2"},
				},
			},
			want: "10.0.0.2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveIP(tt.instance); got != tt.want {
				t.Errorf("resolveIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLookupRunningIP(t *testing.T) {
	// Isolate cache to a temp directory so tests don't pollute each other.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	tests := []struct {
		name    string
		virt    *truenas.MockVirtService
		want    string
		wantErr string
	}{
		{
			name: "running with IP",
			virt: &truenas.MockVirtService{
				GetInstanceFunc: func(_ context.Context, name string) (*truenas.VirtInstance, error) {
					return &truenas.VirtInstance{
						Name:   name,
						Status: "RUNNING",
						Aliases: []truenas.VirtAlias{
							{Type: "INET", Address: "10.0.0.5"},
						},
					}, nil
				},
			},
			want: "10.0.0.5",
		},
		{
			name: "not found",
			virt: &truenas.MockVirtService{
				GetInstanceFunc: func(_ context.Context, _ string) (*truenas.VirtInstance, error) {
					return nil, nil
				},
			},
			wantErr: `pixel "test" not found`,
		},
		{
			name: "stopped",
			virt: &truenas.MockVirtService{
				GetInstanceFunc: func(_ context.Context, name string) (*truenas.VirtInstance, error) {
					return &truenas.VirtInstance{
						Name:   name,
						Status: "STOPPED",
					}, nil
				},
			},
			wantErr: `pixel "test" is STOPPED`,
		},
		{
			name: "running without IP",
			virt: &truenas.MockVirtService{
				GetInstanceFunc: func(_ context.Context, name string) (*truenas.VirtInstance, error) {
					return &truenas.VirtInstance{
						Name:   name,
						Status: "RUNNING",
					}, nil
				},
			},
			wantErr: "no IP address for test",
		},
		{
			name: "API error",
			virt: &truenas.MockVirtService{
				GetInstanceFunc: func(_ context.Context, _ string) (*truenas.VirtInstance, error) {
					return nil, fmt.Errorf("connection refused")
				},
			},
			wantErr: "looking up test: connection refused",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := lookupRunningIP(context.Background(), tt.virt, "test")
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if got := err.Error(); !strings.Contains(got, tt.wantErr) {
					t.Errorf("error = %q, want substring %q", got, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("lookupRunningIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

