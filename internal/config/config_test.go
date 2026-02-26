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
