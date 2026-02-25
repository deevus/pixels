package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	TrueNAS    TrueNAS    `toml:"truenas"`
	Defaults   Defaults   `toml:"defaults"`
	SSH        SSH        `toml:"ssh"`
	Checkpoint Checkpoint `toml:"checkpoint"`
}

type TrueNAS struct {
	Host               string `toml:"host"`
	Port               int    `toml:"port"`
	Username           string `toml:"username"`
	APIKey             string `toml:"api_key"`
	InsecureSkipVerify *bool  `toml:"insecure_skip_verify"`
}

type Defaults struct {
	Image   string `toml:"image"`
	CPU     string `toml:"cpu"`
	Memory  int64  `toml:"memory"` // MiB
	Pool    string `toml:"pool"`
	NICType string `toml:"nic_type"` // "macvlan" or "bridged"
	Parent  string `toml:"parent"`   // parent interface (e.g. "eno1", "br0")
	Network string   `toml:"network"`  // Incus network name (e.g. "incusbr0")
	DNS     []string `toml:"dns"`      // nameservers to write into containers
}

type SSH struct {
	User string `toml:"user"`
	Key  string `toml:"key"`
}

type Checkpoint struct {
	DatasetPrefix string `toml:"dataset_prefix"`
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
			User: "root",
			Key:  "~/.ssh/id_ed25519",
		},
	}

	path := configPath()
	if _, err := os.Stat(path); err == nil {
		if _, err := toml.DecodeFile(path, cfg); err != nil {
			return nil, fmt.Errorf("parsing config %s: %w", path, err)
		}
	}

	applyEnv(&cfg.TrueNAS.Host, "PIXELS_TRUENAS_HOST")
	applyEnv(&cfg.TrueNAS.Username, "PIXELS_TRUENAS_USERNAME")
	applyEnv(&cfg.TrueNAS.APIKey, "PIXELS_TRUENAS_API_KEY")
	applyEnvInt(&cfg.TrueNAS.Port, "PIXELS_TRUENAS_PORT")
	applyEnvBool(&cfg.TrueNAS.InsecureSkipVerify, "PIXELS_TRUENAS_INSECURE")
	applyEnv(&cfg.Defaults.Image, "PIXELS_DEFAULT_IMAGE")
	applyEnv(&cfg.Defaults.CPU, "PIXELS_DEFAULT_CPU")
	applyEnvInt64(&cfg.Defaults.Memory, "PIXELS_DEFAULT_MEMORY")
	applyEnv(&cfg.Defaults.Pool, "PIXELS_DEFAULT_POOL")
	applyEnv(&cfg.SSH.User, "PIXELS_SSH_USER")
	applyEnv(&cfg.SSH.Key, "PIXELS_SSH_KEY")
	applyEnv(&cfg.Checkpoint.DatasetPrefix, "PIXELS_CHECKPOINT_DATASET_PREFIX")

	cfg.SSH.Key = expandHome(cfg.SSH.Key)

	return cfg, nil
}

func configPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "pixels", "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "pixels", "config.toml")
}

func applyEnv(dst *string, key string) {
	if v := os.Getenv(key); v != "" {
		*dst = v
	}
}

func applyEnvInt(dst *int, key string) {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
	}
}

func applyEnvInt64(dst *int64, key string) {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			*dst = n
		}
	}
}

func applyEnvBool(dst **bool, key string) {
	if v := os.Getenv(key); v != "" {
		b, err := strconv.ParseBool(v)
		if err == nil {
			*dst = &b
		}
	}
}

// InsecureSkipVerify returns whether TLS verification should be skipped.
// Defaults to true (skip) when not explicitly set, since most TrueNAS boxes use self-signed certs.
func (t *TrueNAS) InsecureSkipVerifyValue() bool {
	if t.InsecureSkipVerify == nil {
		return true
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
