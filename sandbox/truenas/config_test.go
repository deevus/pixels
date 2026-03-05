package truenas

import (
	"strings"
	"testing"
)

func TestParseCfg(t *testing.T) {
	tests := []struct {
		name    string
		cfg     map[string]string
		check   func(t *testing.T, c *tnConfig)
		wantErr string
	}{
		{
			name: "all defaults with required fields",
			cfg: map[string]string{
				"host":    "truenas.local",
				"api_key": "1-abc",
			},
			check: func(t *testing.T, c *tnConfig) {
				if c.host != "truenas.local" {
					t.Errorf("host = %q", c.host)
				}
				if c.apiKey != "1-abc" {
					t.Errorf("api_key = %q", c.apiKey)
				}
				if c.username != "root" {
					t.Errorf("username = %q, want root", c.username)
				}
				if c.image != "ubuntu/24.04" {
					t.Errorf("image = %q, want ubuntu/24.04", c.image)
				}
				if c.cpu != "2" {
					t.Errorf("cpu = %q, want 2", c.cpu)
				}
				if c.memory != 2048 {
					t.Errorf("memory = %d, want 2048", c.memory)
				}
				if c.pool != "tank" {
					t.Errorf("pool = %q, want tank", c.pool)
				}
				if c.sshUser != "pixel" {
					t.Errorf("ssh_user = %q, want pixel", c.sshUser)
				}
				if c.provision != true {
					t.Error("provision should default to true")
				}
				if c.devtools != true {
					t.Error("devtools should default to true")
				}
				if c.egress != "unrestricted" {
					t.Errorf("egress = %q, want unrestricted", c.egress)
				}
			},
		},
		{
			name: "custom values",
			cfg: map[string]string{
				"host":     "nas.example.com",
				"port":     "8443",
				"api_key":  "2-xyz",
				"username": "admin",
				"insecure": "true",
				"image":    "debian/12",
				"cpu":      "4",
				"memory":   "4096",
				"pool":     "storage",
				"nic_type": "bridged",
				"parent":   "br0",
				"ssh_user": "testuser",
				"ssh_key":  "/tmp/key",
				"egress":   "agent",
				"allow":    "example.com,test.com",
				"dns":      "1.1.1.1,8.8.8.8",
				"provision": "false",
				"devtools":  "false",
				"dataset_prefix": "mypool/virt",
			},
			check: func(t *testing.T, c *tnConfig) {
				if c.port != 8443 {
					t.Errorf("port = %d", c.port)
				}
				if c.username != "admin" {
					t.Errorf("username = %q", c.username)
				}
				if c.insecure != true {
					t.Error("insecure should be true")
				}
				if c.image != "debian/12" {
					t.Errorf("image = %q", c.image)
				}
				if c.cpu != "4" {
					t.Errorf("cpu = %q", c.cpu)
				}
				if c.memory != 4096 {
					t.Errorf("memory = %d", c.memory)
				}
				if c.pool != "storage" {
					t.Errorf("pool = %q", c.pool)
				}
				if c.nicType != "bridged" {
					t.Errorf("nic_type = %q", c.nicType)
				}
				if c.parent != "br0" {
					t.Errorf("parent = %q", c.parent)
				}
				if c.sshUser != "testuser" {
					t.Errorf("ssh_user = %q", c.sshUser)
				}
				if c.sshKey != "/tmp/key" {
					t.Errorf("ssh_key = %q", c.sshKey)
				}
				if c.provision != false {
					t.Error("provision should be false")
				}
				if c.devtools != false {
					t.Error("devtools should be false")
				}
				if c.egress != "agent" {
					t.Errorf("egress = %q", c.egress)
				}
				if len(c.allow) != 2 || c.allow[0] != "example.com" || c.allow[1] != "test.com" {
					t.Errorf("allow = %v", c.allow)
				}
				if len(c.dns) != 2 || c.dns[0] != "1.1.1.1" {
					t.Errorf("dns = %v", c.dns)
				}
				if c.datasetPrefix != "mypool/virt" {
					t.Errorf("dataset_prefix = %q", c.datasetPrefix)
				}
			},
		},
		{
			name:    "missing host",
			cfg:     map[string]string{"api_key": "abc"},
			wantErr: "host is required",
		},
		{
			name:    "missing api_key",
			cfg:     map[string]string{"host": "nas.local"},
			wantErr: "api_key is required",
		},
		{
			name:    "invalid port",
			cfg:     map[string]string{"host": "nas", "api_key": "k", "port": "abc"},
			wantErr: "invalid port",
		},
		{
			name:    "invalid memory",
			cfg:     map[string]string{"host": "nas", "api_key": "k", "memory": "big"},
			wantErr: "invalid memory",
		},
		{
			name:    "invalid insecure",
			cfg:     map[string]string{"host": "nas", "api_key": "k", "insecure": "maybe"},
			wantErr: "invalid insecure",
		},
		{
			name:    "invalid provision",
			cfg:     map[string]string{"host": "nas", "api_key": "k", "provision": "sometimes"},
			wantErr: "invalid provision",
		},
		{
			name:    "invalid devtools",
			cfg:     map[string]string{"host": "nas", "api_key": "k", "devtools": "yes"},
			wantErr: "invalid devtools",
		},
		{
			name:    "invalid egress mode",
			cfg:     map[string]string{"host": "nas", "api_key": "k", "egress": "deny-all"},
			wantErr: "invalid egress",
		},
		{
			name: "env_forward_keys parsed",
			cfg: map[string]string{
				"host":              "nas.test",
				"api_key":           "key",
				"env_forward_keys":  "GITHUB_TOKEN,API_KEY",
			},
			check: func(t *testing.T, c *tnConfig) {
				if len(c.envForwardKeys) != 2 {
					t.Fatalf("envForwardKeys len = %d, want 2", len(c.envForwardKeys))
				}
				if c.envForwardKeys[0] != "GITHUB_TOKEN" || c.envForwardKeys[1] != "API_KEY" {
					t.Errorf("envForwardKeys = %v", c.envForwardKeys)
				}
			},
		},
		{
			name: "env_forward_keys empty",
			cfg: map[string]string{
				"host":    "nas.test",
				"api_key": "key",
			},
			check: func(t *testing.T, c *tnConfig) {
				if c.envForwardKeys != nil {
					t.Errorf("envForwardKeys = %v, want nil", c.envForwardKeys)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := parseCfg(tt.cfg)
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
			if tt.check != nil {
				tt.check(t, c)
			}
		})
	}
}

