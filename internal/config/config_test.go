package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	// Point XDG to a nonexistent dir so no config file is loaded.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Clear any env overrides.
	for _, key := range []string{
		"PIXELS_TRUENAS_HOST", "PIXELS_TRUENAS_USERNAME", "PIXELS_TRUENAS_API_KEY",
		"PIXELS_DEFAULT_IMAGE", "PIXELS_DEFAULT_CPU", "PIXELS_DEFAULT_MEMORY",
		"PIXELS_DEFAULT_POOL", "PIXELS_SSH_USER", "PIXELS_SSH_KEY",
		"PIXELS_CHECKPOINT_DATASET_PREFIX",
	} {
		t.Setenv(key, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Defaults.Image != "ubuntu/24.04" {
		t.Errorf("image = %q, want %q", cfg.Defaults.Image, "ubuntu/24.04")
	}
	if cfg.Defaults.CPU != "2" {
		t.Errorf("cpu = %q, want %q", cfg.Defaults.CPU, "2")
	}
	if cfg.Defaults.Memory != 2048 {
		t.Errorf("memory = %d, want %d", cfg.Defaults.Memory, 2048)
	}
	if cfg.Defaults.Pool != "tank" {
		t.Errorf("pool = %q, want %q", cfg.Defaults.Pool, "tank")
	}
	if cfg.SSH.User != "pixel" {
		t.Errorf("ssh.user = %q, want %q", cfg.SSH.User, "pixel")
	}
	if cfg.TrueNAS.Username != "root" {
		t.Errorf("truenas.username = %q, want %q", cfg.TrueNAS.Username, "root")
	}
	if cfg.TrueNAS.InsecureSkipVerifyValue() {
		t.Error("InsecureSkipVerifyValue() = true, want false (default)")
	}

	// Provision defaults: enabled when not set.
	if !cfg.Provision.IsEnabled() {
		t.Error("Provision.IsEnabled() = false, want true (default)")
	}
	if !cfg.Provision.DevToolsEnabled() {
		t.Error("Provision.DevToolsEnabled() = false, want true (default)")
	}

	// Env defaults: nil when not configured.
	if cfg.Env != nil {
		t.Errorf("Env = %v, want nil", cfg.Env)
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfgDir := filepath.Join(dir, "pixels")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `
[truenas]
host = "nas.home"
api_key = "1-abc123"

[defaults]
image = "debian/12"
cpu = "4"
memory = 4096
pool = "ssd"

[ssh]
user = "admin"
key = "/tmp/test_key"

[checkpoint]
dataset_prefix = "ssd/custom/containers"

[provision]
enabled = true
devtools = false

[env]
FOO = "bar"
BAZ = "qux"
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Clear env overrides.
	for _, key := range []string{
		"PIXELS_TRUENAS_HOST", "PIXELS_TRUENAS_API_KEY",
		"PIXELS_DEFAULT_IMAGE", "PIXELS_DEFAULT_CPU", "PIXELS_DEFAULT_MEMORY",
		"PIXELS_DEFAULT_POOL", "PIXELS_SSH_USER", "PIXELS_SSH_KEY",
		"PIXELS_CHECKPOINT_DATASET_PREFIX",
	} {
		t.Setenv(key, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.TrueNAS.Host != "nas.home" {
		t.Errorf("host = %q, want %q", cfg.TrueNAS.Host, "nas.home")
	}
	if cfg.TrueNAS.APIKey != "1-abc123" {
		t.Errorf("api_key = %q, want %q", cfg.TrueNAS.APIKey, "1-abc123")
	}
	if cfg.Defaults.Image != "debian/12" {
		t.Errorf("image = %q, want %q", cfg.Defaults.Image, "debian/12")
	}
	if cfg.Defaults.CPU != "4" {
		t.Errorf("cpu = %q, want %q", cfg.Defaults.CPU, "4")
	}
	if cfg.Defaults.Memory != 4096 {
		t.Errorf("memory = %d, want %d", cfg.Defaults.Memory, 4096)
	}
	if cfg.Defaults.Pool != "ssd" {
		t.Errorf("pool = %q, want %q", cfg.Defaults.Pool, "ssd")
	}
	if cfg.SSH.Key != "/tmp/test_key" {
		t.Errorf("ssh.key = %q, want %q", cfg.SSH.Key, "/tmp/test_key")
	}
	if cfg.Checkpoint.DatasetPrefix != "ssd/custom/containers" {
		t.Errorf("dataset_prefix = %q, want %q", cfg.Checkpoint.DatasetPrefix, "ssd/custom/containers")
	}

	// Provision section from TOML.
	if !cfg.Provision.IsEnabled() {
		t.Error("Provision.IsEnabled() = false, want true")
	}
	if cfg.Provision.DevToolsEnabled() {
		t.Error("Provision.DevToolsEnabled() = true, want false")
	}

	// Env section from TOML.
	if cfg.Env["FOO"] != "bar" {
		t.Errorf("Env[FOO] = %q, want %q", cfg.Env["FOO"], "bar")
	}
	if cfg.Env["BAZ"] != "qux" {
		t.Errorf("Env[BAZ] = %q, want %q", cfg.Env["BAZ"], "qux")
	}
}

func TestEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfgDir := filepath.Join(dir, "pixels")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `
[truenas]
host = "file-host"
api_key = "file-key"
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PIXELS_TRUENAS_HOST", "env-host")
	t.Setenv("PIXELS_TRUENAS_API_KEY", "env-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.TrueNAS.Host != "env-host" {
		t.Errorf("host = %q, want %q (env should override file)", cfg.TrueNAS.Host, "env-host")
	}
	if cfg.TrueNAS.APIKey != "env-key" {
		t.Errorf("api_key = %q, want %q (env should override file)", cfg.TrueNAS.APIKey, "env-key")
	}
}

func TestEmptyEnvDoesNotOverrideDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PIXELS_DEFAULT_IMAGE", "")
	t.Setenv("PIXELS_DEFAULT_POOL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Defaults.Image != "ubuntu/24.04" {
		t.Errorf("image = %q, want %q (empty env should not override default)", cfg.Defaults.Image, "ubuntu/24.04")
	}
	if cfg.Defaults.Pool != "tank" {
		t.Errorf("pool = %q, want %q (empty env should not override default)", cfg.Defaults.Pool, "tank")
	}
}

func TestInvalidEnvReturnsError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PIXELS_TRUENAS_PORT", "not-a-number")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid PIXELS_TRUENAS_PORT, got nil")
	}
}

func TestProvisionEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfgDir := filepath.Join(dir, "pixels")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// TOML has provision enabled.
	content := `
[provision]
enabled = true
devtools = true
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Env var overrides to disabled.
	t.Setenv("PIXELS_PROVISION_ENABLED", "false")
	t.Setenv("PIXELS_PROVISION_DEVTOOLS", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Provision.IsEnabled() {
		t.Error("Provision.IsEnabled() = true, want false (env should override)")
	}
	if cfg.Provision.DevToolsEnabled() {
		t.Error("Provision.DevToolsEnabled() = true, want false (env should override)")
	}
}

func TestEnvExpansion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfgDir := filepath.Join(dir, "pixels")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `
[env]
MY_KEY = "$PIXELS_TEST_SECRET"
LITERAL = "no-expansion-here"
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PIXELS_TEST_SECRET", "sk-secret-123")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Env["MY_KEY"] != "sk-secret-123" {
		t.Errorf("Env[MY_KEY] = %q, want %q (should expand $PIXELS_TEST_SECRET)", cfg.Env["MY_KEY"], "sk-secret-123")
	}
	if cfg.Env["LITERAL"] != "no-expansion-here" {
		t.Errorf("Env[LITERAL] = %q, want %q", cfg.Env["LITERAL"], "no-expansion-here")
	}
}

func TestEnvForward(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfgDir := filepath.Join(dir, "pixels")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `
[env]
IMAGE_VAR = "baked-in"
EXPLICIT_IMAGE = { value = "explicit" }
FORWARDED = { forward = true }
FORWARDED_UNSET = { forward = true }
SESSION_LITERAL = { value = "session-val", session_only = true }
EXPANDED_IMAGE = { value = "$PIXELS_TEST_EXPAND" }
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("FORWARDED", "host-value")
	// FORWARDED_UNSET intentionally not set.
	t.Setenv("PIXELS_TEST_EXPAND", "expanded-val")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Image vars (cfg.Env).
	if cfg.Env["IMAGE_VAR"] != "baked-in" {
		t.Errorf("Env[IMAGE_VAR] = %q, want %q", cfg.Env["IMAGE_VAR"], "baked-in")
	}
	if cfg.Env["EXPLICIT_IMAGE"] != "explicit" {
		t.Errorf("Env[EXPLICIT_IMAGE] = %q, want %q", cfg.Env["EXPLICIT_IMAGE"], "explicit")
	}
	if cfg.Env["EXPANDED_IMAGE"] != "expanded-val" {
		t.Errorf("Env[EXPANDED_IMAGE] = %q, want %q", cfg.Env["EXPANDED_IMAGE"], "expanded-val")
	}

	// Image vars should NOT include session entries.
	if _, ok := cfg.Env["FORWARDED"]; ok {
		t.Error("Env should not contain FORWARDED (it's a session var)")
	}
	if _, ok := cfg.Env["SESSION_LITERAL"]; ok {
		t.Error("Env should not contain SESSION_LITERAL (it's a session var)")
	}

	// Session vars (cfg.EnvForward).
	if cfg.EnvForward["FORWARDED"] != "host-value" {
		t.Errorf("EnvForward[FORWARDED] = %q, want %q", cfg.EnvForward["FORWARDED"], "host-value")
	}
	if _, ok := cfg.EnvForward["FORWARDED_UNSET"]; ok {
		t.Error("EnvForward should not contain FORWARDED_UNSET (not set on host)")
	}
	if cfg.EnvForward["SESSION_LITERAL"] != "session-val" {
		t.Errorf("EnvForward[SESSION_LITERAL] = %q, want %q", cfg.EnvForward["SESSION_LITERAL"], "session-val")
	}

	// Session vars should NOT include image entries.
	if _, ok := cfg.EnvForward["IMAGE_VAR"]; ok {
		t.Error("EnvForward should not contain IMAGE_VAR (it's an image var)")
	}
}

func TestEnvForwardNilWhenEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Env != nil {
		t.Errorf("Env = %v, want nil (no env configured)", cfg.Env)
	}
	if cfg.EnvForward != nil {
		t.Errorf("EnvForward = %v, want nil (no env configured)", cfg.EnvForward)
	}
}

func TestEnvUnsupportedType(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfgDir := filepath.Join(dir, "pixels")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// An integer value in [env] is not a valid env entry.
	content := `
[env]
BAD = 42
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for unsupported env type, got nil")
	}
}

func TestNetworkDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, key := range []string{
		"PIXELS_TRUENAS_HOST", "PIXELS_TRUENAS_USERNAME", "PIXELS_TRUENAS_API_KEY",
		"PIXELS_NETWORK_EGRESS",
	} {
		t.Setenv(key, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Network.Egress != "unrestricted" {
		t.Errorf("Network.Egress = %q, want %q", cfg.Network.Egress, "unrestricted")
	}
	if cfg.Network.Allow != nil {
		t.Errorf("Network.Allow = %v, want nil", cfg.Network.Allow)
	}
}

func TestNetworkFromFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfgDir := filepath.Join(dir, "pixels")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `
[network]
egress = "agent"
allow = ["internal.mycompany.com", "registry.example.com"]
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PIXELS_NETWORK_EGRESS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Network.Egress != "agent" {
		t.Errorf("Network.Egress = %q, want %q", cfg.Network.Egress, "agent")
	}
	if len(cfg.Network.Allow) != 2 {
		t.Fatalf("Network.Allow len = %d, want 2", len(cfg.Network.Allow))
	}
	if cfg.Network.Allow[0] != "internal.mycompany.com" {
		t.Errorf("Network.Allow[0] = %q, want %q", cfg.Network.Allow[0], "internal.mycompany.com")
	}
}

func TestNetworkEnvOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PIXELS_NETWORK_EGRESS", "allowlist")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Network.Egress != "allowlist" {
		t.Errorf("Network.Egress = %q, want %q", cfg.Network.Egress, "allowlist")
	}
}

func TestNetworkIsRestricted(t *testing.T) {
	tests := []struct {
		egress string
		want   bool
	}{
		{"unrestricted", false},
		{"agent", true},
		{"allowlist", true},
	}
	for _, tt := range tests {
		t.Run(tt.egress, func(t *testing.T) {
			n := Network{Egress: tt.egress}
			if got := n.IsRestricted(); got != tt.want {
				t.Errorf("IsRestricted() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfigPathXDG(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	got := configPath()
	want := filepath.Join(dir, "pixels", "config.toml")
	if got != want {
		t.Errorf("configPath() = %q, want %q", got, want)
	}
}

func TestConfigPathDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	got := configPath()
	configDir, _ := os.UserConfigDir()
	want := filepath.Join(configDir, "pixels", "config.toml")
	if got != want {
		t.Errorf("configPath() = %q, want %q", got, want)
	}
}

func TestStrictHostKeysEnabled(t *testing.T) {
	tests := []struct {
		name string
		val  *bool
		want bool
	}{
		{"nil defaults to true", nil, true},
		{"explicit true", boolPtr(true), true},
		{"explicit false", boolPtr(false), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := SSH{StrictHostKeys: tt.val}
			if got := s.StrictHostKeysEnabled(); got != tt.want {
				t.Errorf("StrictHostKeysEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func boolPtr(v bool) *bool { return &v }

func TestStrictHostKeysFromFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfgDir := filepath.Join(dir, "pixels")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `
[ssh]
strict_host_keys = false
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.SSH.StrictHostKeysEnabled() {
		t.Error("StrictHostKeysEnabled() = true, want false (set in TOML)")
	}
}

func TestStrictHostKeysEnvOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PIXELS_SSH_STRICT_HOST_KEYS", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.SSH.StrictHostKeysEnabled() {
		t.Error("StrictHostKeysEnabled() = true, want false (env override)")
	}
}

func TestKnownHostsPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	got := KnownHostsPath()
	want := filepath.Join(dir, "pixels", "known_hosts")
	if got != want {
		t.Errorf("KnownHostsPath() = %q, want %q", got, want)
	}
}

func TestMCPDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got, want := cfg.MCP.Prefix, "px-mcp-"; got != want {
		t.Errorf("Prefix = %q, want %q", got, want)
	}
	if got, want := cfg.MCP.IdleStopAfter, "1h"; got != want {
		t.Errorf("IdleStopAfter = %q, want %q", got, want)
	}
	if got, want := cfg.MCP.HardDestroyAfter, "24h"; got != want {
		t.Errorf("HardDestroyAfter = %q, want %q", got, want)
	}
	if got, want := cfg.MCP.ListenAddr, "127.0.0.1:8765"; got != want {
		t.Errorf("ListenAddr = %q, want %q", got, want)
	}
}

func TestMCPEnvOverride(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("PIXELS_MCP_LISTEN_ADDR", "0.0.0.0:9000")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.MCP.ListenAddr, "0.0.0.0:9000"; got != want {
		t.Errorf("ListenAddr = %q, want %q", got, want)
	}
}

func TestMCPTOMLOverride(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	cfgPath := filepath.Join(tmpDir, "pixels", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(`
[mcp]
prefix = "test-"
idle_stop_after = "30m"
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.MCP.Prefix, "test-"; got != want {
		t.Errorf("Prefix = %q, want %q", got, want)
	}
	if got, want := cfg.MCP.IdleStopAfter, "30m"; got != want {
		t.Errorf("IdleStopAfter = %q, want %q", got, want)
	}
}

func TestMCPStateFilePath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	cfg := &Config{}
	got := cfg.MCPStateFile()
	want := filepath.Join(tmpDir, "pixels", "mcp-state.json")
	if got != want {
		t.Errorf("MCPStateFile = %q, want %q", got, want)
	}
}

func TestMCPStateFilePathOverride(t *testing.T) {
	cfg := &Config{MCP: MCP{StateFile: "/custom/state.json"}}
	if got, want := cfg.MCPStateFile(), "/custom/state.json"; got != want {
		t.Errorf("MCPStateFile = %q, want %q", got, want)
	}
}

func TestMCPBasesParsed(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	cfgPath := filepath.Join(tmpDir, "pixels", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(`
[mcp.bases.python]
parent_image = "images:ubuntu/24.04"
setup_script = "~/scripts/python.sh"
description = "Python 3"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	b, ok := cfg.MCP.Bases["python"]
	if !ok {
		t.Fatalf("python base not parsed; got %+v", cfg.MCP.Bases)
	}
	if b.ParentImage != "images:ubuntu/24.04" {
		t.Errorf("ParentImage = %q", b.ParentImage)
	}
	if b.Description != "Python 3" {
		t.Errorf("Description = %q", b.Description)
	}
	// SetupScript should have ~ expanded.
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "scripts/python.sh")
	if b.SetupScript != want {
		t.Errorf("SetupScript = %q, want %q (expanded)", b.SetupScript, want)
	}
}

func TestMCPPIDFilePath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	cfg := &Config{}
	got := cfg.MCPPIDFile()
	want := filepath.Join(tmpDir, "pixels", "mcp.pid")
	if got != want {
		t.Errorf("MCPPIDFile = %q, want %q", got, want)
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}

	got := expandHome("~/.ssh/id_ed25519")
	want := filepath.Join(home, ".ssh/id_ed25519")
	if got != want {
		t.Errorf("expandHome(~/.ssh/id_ed25519) = %q, want %q", got, want)
	}

	abs := "/absolute/path"
	if expandHome(abs) != abs {
		t.Errorf("expandHome(%q) should return unchanged", abs)
	}
}

func TestMCPBasePrefixDefault(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.MCP.BasePrefix, "px-base-"; got != want {
		t.Errorf("BasePrefix = %q, want %q", got, want)
	}
}

func TestMCPBasePrefixEnvOverride(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("PIXELS_MCP_BASE_PREFIX", "custom-")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.MCP.BasePrefix, "custom-"; got != want {
		t.Errorf("BasePrefix = %q, want %q", got, want)
	}
}

func TestBaseFromField(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	cfgPath := filepath.Join(tmpDir, "pixels", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(`
[mcp.bases.dev]
parent_image = "images:ubuntu/24.04"
setup_script = "/tmp/dev.sh"
description = "Dev"

[mcp.bases.python]
from = "dev"
setup_script = "/tmp/python.sh"
description = "Python"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.MCP.Bases["python"].From; got != "dev" {
		t.Errorf("python.From = %q, want dev", got)
	}
	if got := cfg.MCP.Bases["dev"].ParentImage; got != "images:ubuntu/24.04" {
		t.Errorf("dev.ParentImage = %q", got)
	}
}

func TestBaseRejectsBothParentImageAndFrom(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	cfgPath := filepath.Join(tmpDir, "pixels", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(`
[mcp.bases.bad]
parent_image = "images:ubuntu/24.04"
from = "dev"
setup_script = "/tmp/x.sh"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load()
	if err == nil {
		t.Fatal("expected Load to error on base with both parent_image and from")
	}
}

func TestBaseRejectsNeitherParentImageNorFrom(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	cfgPath := filepath.Join(tmpDir, "pixels", "config.toml")
	_ = os.MkdirAll(filepath.Dir(cfgPath), 0o755)
	_ = os.WriteFile(cfgPath, []byte(`
[mcp.bases.bad]
setup_script = "/tmp/x.sh"
`), 0o644)
	_, err := Load()
	if err == nil {
		t.Fatal("expected Load to error on base with neither parent_image nor from")
	}
}

func TestBaseRejectsFromMissingTarget(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	cfgPath := filepath.Join(tmpDir, "pixels", "config.toml")
	_ = os.MkdirAll(filepath.Dir(cfgPath), 0o755)
	_ = os.WriteFile(cfgPath, []byte(`
[mcp.bases.python]
from = "doesnotexist"
setup_script = "/tmp/x.sh"
`), 0o644)
	_, err := Load()
	if err == nil {
		t.Fatal("expected Load to error on base whose from references a missing base")
	}
}

func TestBaseRejectsCycle(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	cfgPath := filepath.Join(tmpDir, "pixels", "config.toml")
	_ = os.MkdirAll(filepath.Dir(cfgPath), 0o755)
	_ = os.WriteFile(cfgPath, []byte(`
[mcp.bases.a]
from = "b"
setup_script = "/tmp/x.sh"

[mcp.bases.b]
from = "a"
setup_script = "/tmp/x.sh"
`), 0o644)
	_, err := Load()
	if err == nil {
		t.Fatal("expected Load to error on base cycle")
	}
}

func TestBaseAcceptsValidChain(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	cfgPath := filepath.Join(tmpDir, "pixels", "config.toml")
	_ = os.MkdirAll(filepath.Dir(cfgPath), 0o755)
	_ = os.WriteFile(cfgPath, []byte(`
[mcp.bases.dev]
parent_image = "images:ubuntu/24.04"
setup_script = "/tmp/dev.sh"

[mcp.bases.python]
from = "dev"
setup_script = "/tmp/python.sh"

[mcp.bases.django]
from = "python"
setup_script = "/tmp/django.sh"
`), 0o644)
	_, err := Load()
	if err != nil {
		t.Fatalf("expected valid chain to load, got: %v", err)
	}
}

func TestDefaultBasesMergedWhenAbsentFromUserConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dev", "python", "node"} {
		if _, ok := cfg.MCP.Bases[name]; !ok {
			t.Errorf("default base %q should be present after Load with no user config", name)
		}
	}
}

func TestUserConfigReplacesDefault(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	cfgPath := filepath.Join(tmpDir, "pixels", "config.toml")
	_ = os.MkdirAll(filepath.Dir(cfgPath), 0o755)
	_ = os.WriteFile(cfgPath, []byte(`
[mcp.bases.python]
parent_image = "images:debian/12"
setup_script = "/tmp/my-python.sh"
description = "my python"
`), 0o644)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.MCP.Bases["python"]
	if got.ParentImage != "images:debian/12" {
		t.Errorf("user config did not win: ParentImage = %q", got.ParentImage)
	}
	if got.From != "" {
		t.Errorf("user config did not fully replace default: From = %q (default had from='dev')", got.From)
	}
	if got.Description != "my python" {
		t.Errorf("Description = %q", got.Description)
	}
	// Other defaults still present
	if _, ok := cfg.MCP.Bases["dev"]; !ok {
		t.Errorf("dev (untouched default) should still be present")
	}
}
