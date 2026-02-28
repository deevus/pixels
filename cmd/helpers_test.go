package cmd

import (
	"testing"

	truenas "github.com/deevus/truenas-go"
)

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
