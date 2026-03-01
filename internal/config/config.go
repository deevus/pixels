package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/caarlos0/env/v11"
)

type Config struct {
	TrueNAS    TrueNAS        `toml:"truenas"`
	Defaults   Defaults       `toml:"defaults"`
	SSH        SSH            `toml:"ssh"`
	Checkpoint Checkpoint     `toml:"checkpoint"`
	Provision  Provision      `toml:"provision"`
	Network    Network        `toml:"network"`
	RawEnv     map[string]any `toml:"env"`

	// Resolved env vars (not from TOML directly).
	Env        map[string]string `toml:"-"` // image vars → /etc/environment
	EnvForward map[string]string `toml:"-"` // session vars → SSH SetEnv
}

type TrueNAS struct {
	Host               string `toml:"host"                env:"PIXELS_TRUENAS_HOST"`
	Port               int    `toml:"port"                env:"PIXELS_TRUENAS_PORT"`
	Username           string `toml:"username"            env:"PIXELS_TRUENAS_USERNAME"`
	APIKey             string `toml:"api_key"             env:"PIXELS_TRUENAS_API_KEY"`
	InsecureSkipVerify *bool  `toml:"insecure_skip_verify" env:"PIXELS_TRUENAS_INSECURE"`
}

type Defaults struct {
	Image   string   `toml:"image"    env:"PIXELS_DEFAULT_IMAGE"`
	CPU     string   `toml:"cpu"      env:"PIXELS_DEFAULT_CPU"`
	Memory  int64    `toml:"memory"   env:"PIXELS_DEFAULT_MEMORY"` // MiB
	Pool    string   `toml:"pool"     env:"PIXELS_DEFAULT_POOL"`
	NICType string   `toml:"nic_type"` // "macvlan" or "bridged"
	Parent  string   `toml:"parent"`   // parent interface (e.g. "eno1", "br0")
	Network string   `toml:"network"`  // Incus network name (e.g. "incusbr0")
	DNS     []string `toml:"dns"`      // nameservers to write into containers
}

type SSH struct {
	User string `toml:"user" env:"PIXELS_SSH_USER"`
	Key  string `toml:"key"  env:"PIXELS_SSH_KEY"`
}

type Checkpoint struct {
	DatasetPrefix string `toml:"dataset_prefix" env:"PIXELS_CHECKPOINT_DATASET_PREFIX"`
}

type Provision struct {
	Enabled  *bool `toml:"enabled"  env:"PIXELS_PROVISION_ENABLED"`
	DevTools *bool `toml:"devtools" env:"PIXELS_PROVISION_DEVTOOLS"`
}

func (p *Provision) IsEnabled() bool {
	if p.Enabled == nil {
		return true
	}
	return *p.Enabled
}

func (p *Provision) DevToolsEnabled() bool {
	if p.DevTools == nil {
		return true
	}
	return *p.DevTools
}

type Network struct {
	Egress string   `toml:"egress" env:"PIXELS_NETWORK_EGRESS"`
	Allow  []string `toml:"allow"`
}

func (n *Network) IsRestricted() bool {
	return n.Egress == "agent" || n.Egress == "allowlist"
}

func Load() (*Config, error) {
	cfg := &Config{
		TrueNAS: TrueNAS{
			Username: "root",
		},
		Defaults: Defaults{
			Image:  "ubuntu/24.04",
			CPU:    "2",
			Memory: 2048,
			Pool:   "tank",
		},
		SSH: SSH{
			User: "pixel",
			Key:  "~/.ssh/id_ed25519",
		},
		Network: Network{
			Egress: "unrestricted",
		},
	}

	path := configPath()
	if _, err := os.Stat(path); err == nil {
		if _, err := toml.DecodeFile(path, cfg); err != nil {
			return nil, fmt.Errorf("parsing config %s: %w", path, err)
		}
	}

	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("parsing environment: %w", err)
	}

	cfg.SSH.Key = expandHome(cfg.SSH.Key)

	if err := resolveEnv(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// resolveEnv splits RawEnv entries into image vars (Env) and session vars (EnvForward).
//
// Supported forms:
//
//	KEY = "value"                          → image var (with $VAR expansion)
//	KEY = { value = "x" }                 → image var (with $VAR expansion)
//	KEY = { forward = true }              → session var (from host env, skip if unset)
//	KEY = { value = "x", session_only = true } → session var (literal, with $VAR expansion)
func resolveEnv(cfg *Config) error {
	if len(cfg.RawEnv) == 0 {
		return nil
	}

	cfg.Env = make(map[string]string)
	cfg.EnvForward = make(map[string]string)

	for k, raw := range cfg.RawEnv {
		switch v := raw.(type) {
		case string:
			cfg.Env[k] = os.ExpandEnv(v)
		case map[string]any:
			forward, _ := v["forward"].(bool)
			sessionOnly, _ := v["session_only"].(bool)
			value, _ := v["value"].(string)

			switch {
			case forward:
				if hostVal, ok := os.LookupEnv(k); ok {
					cfg.EnvForward[k] = hostVal
				}
			case sessionOnly && value != "":
				cfg.EnvForward[k] = os.ExpandEnv(value)
			case value != "":
				cfg.Env[k] = os.ExpandEnv(value)
			}
		default:
			return fmt.Errorf("env %q: unsupported type %T", k, raw)
		}
	}

	if len(cfg.Env) == 0 {
		cfg.Env = nil
	}
	if len(cfg.EnvForward) == 0 {
		cfg.EnvForward = nil
	}

	return nil
}

func configPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "pixels", "config.toml")
	}
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "pixels", "config.toml")
}

// InsecureSkipVerify returns whether TLS verification should be skipped.
// Defaults to true (skip) when not explicitly set, since most TrueNAS boxes use self-signed certs.
func (t *TrueNAS) InsecureSkipVerifyValue() bool {
	if t.InsecureSkipVerify == nil {
		return false
	}
	return *t.InsecureSkipVerify
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
