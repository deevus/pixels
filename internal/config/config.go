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
	Backend    string         `toml:"backend"    env:"PIXELS_BACKEND"` // "truenas" or "incus"
	TrueNAS    TrueNAS        `toml:"truenas"`
	Incus      Incus          `toml:"incus"`
	Defaults   Defaults       `toml:"defaults"`
	SSH        SSH            `toml:"ssh"`
	Checkpoint Checkpoint     `toml:"checkpoint"`
	Provision  Provision      `toml:"provision"`
	Network    Network        `toml:"network"`
	MCP        MCP            `toml:"mcp"`
	RawEnv     map[string]any `toml:"env"`

	// Resolved env vars (not from TOML directly).
	Env        map[string]string `toml:"-"` // image vars → /etc/environment
	EnvForward map[string]string `toml:"-"` // session vars → SSH SetEnv
}

type Incus struct {
	Socket     string `toml:"socket"      env:"PIXELS_INCUS_SOCKET"`
	Remote     string `toml:"remote"      env:"PIXELS_INCUS_REMOTE"`
	ClientCert string `toml:"client_cert" env:"PIXELS_INCUS_CLIENT_CERT"`
	ClientKey  string `toml:"client_key"  env:"PIXELS_INCUS_CLIENT_KEY"`
	ServerCert string `toml:"server_cert" env:"PIXELS_INCUS_SERVER_CERT"`
	Project    string `toml:"project"     env:"PIXELS_INCUS_PROJECT"`
}

type TrueNAS struct {
	Host               string `toml:"host"                env:"PIXELS_TRUENAS_HOST"`
	Port               int    `toml:"port"                env:"PIXELS_TRUENAS_PORT"`
	Username           string `toml:"username"            env:"PIXELS_TRUENAS_USERNAME"`
	APIKey             string `toml:"api_key"             env:"PIXELS_TRUENAS_API_KEY"`
	InsecureSkipVerify *bool  `toml:"insecure_skip_verify" env:"PIXELS_TRUENAS_INSECURE"`
}

type Base struct {
	ParentImage string `toml:"parent_image"`
	From        string `toml:"from"`
	SetupScript string `toml:"setup_script"`
	Description string `toml:"description"`
}

type MCP struct {
	Prefix           string          `toml:"prefix"             env:"PIXELS_MCP_PREFIX"`
	BasePrefix       string          `toml:"base_prefix"        env:"PIXELS_MCP_BASE_PREFIX"`
	DefaultImage     string          `toml:"default_image"      env:"PIXELS_MCP_DEFAULT_IMAGE"`
	IdleStopAfter    string          `toml:"idle_stop_after"    env:"PIXELS_MCP_IDLE_STOP_AFTER"`
	HardDestroyAfter string          `toml:"hard_destroy_after" env:"PIXELS_MCP_HARD_DESTROY_AFTER"`
	ReapInterval     string          `toml:"reap_interval"      env:"PIXELS_MCP_REAP_INTERVAL"`
	StateFile        string          `toml:"state_file"         env:"PIXELS_MCP_STATE_FILE"`
	PIDFile          string          `toml:"pid_file"           env:"PIXELS_MCP_PID_FILE"`
	ExecTimeoutMax   string          `toml:"exec_timeout_max"   env:"PIXELS_MCP_EXEC_TIMEOUT_MAX"`
	ListenAddr       string          `toml:"listen_addr"        env:"PIXELS_MCP_LISTEN_ADDR"`
	EndpointPath     string          `toml:"endpoint_path"      env:"PIXELS_MCP_ENDPOINT_PATH"`
	Bases            map[string]Base `toml:"bases"`
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
	User           string `toml:"user"             env:"PIXELS_SSH_USER"`
	Key            string `toml:"key"              env:"PIXELS_SSH_KEY"`
	StrictHostKeys *bool  `toml:"strict_host_keys" env:"PIXELS_SSH_STRICT_HOST_KEYS"`
}

// StrictHostKeysEnabled returns whether SSH host key verification is enabled.
// Defaults to true when not explicitly set.
func (s *SSH) StrictHostKeysEnabled() bool {
	if s.StrictHostKeys == nil {
		return true
	}
	return *s.StrictHostKeys
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
		Backend: "incus",
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
		MCP: MCP{
			Prefix:           "px-mcp-",
			BasePrefix:       "px-base-",
			IdleStopAfter:    "1h",
			HardDestroyAfter: "24h",
			ReapInterval:     "1m",
			ExecTimeoutMax:   "10m",
			ListenAddr:       "127.0.0.1:8765",
			EndpointPath:     "/mcp",
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
	cfg.Incus.Socket = expandHome(cfg.Incus.Socket)
	cfg.Incus.ClientCert = expandHome(cfg.Incus.ClientCert)
	cfg.Incus.ClientKey = expandHome(cfg.Incus.ClientKey)
	cfg.Incus.ServerCert = expandHome(cfg.Incus.ServerCert)

	for name, b := range cfg.MCP.Bases {
		b.SetupScript = expandHome(b.SetupScript)
		cfg.MCP.Bases[name] = b
	}

	// Merge default bases into config: user config wins on name conflict.
	if cfg.MCP.Bases == nil {
		cfg.MCP.Bases = make(map[string]Base)
	}
	for name, b := range DefaultBases {
		if _, ok := cfg.MCP.Bases[name]; ok {
			continue // user config wins
		}
		cfg.MCP.Bases[name] = b
	}

	if err := resolveEnv(cfg); err != nil {
		return nil, err
	}

	if err := validateBases(cfg.MCP.Bases); err != nil {
		return nil, err
	}

	return cfg, nil
}

// validateBases checks that every [mcp.bases] entry declares exactly one of
// parent_image or from, that every from references an existing base, and
// that the dependency graph has no cycles.
func validateBases(bases map[string]Base) error {
	if len(bases) == 0 {
		return nil
	}
	for name, b := range bases {
		hasParent := b.ParentImage != ""
		hasFrom := b.From != ""
		if hasParent && hasFrom {
			return fmt.Errorf("mcp.bases.%s: parent_image and from are mutually exclusive", name)
		}
		if !hasParent && !hasFrom {
			return fmt.Errorf("mcp.bases.%s: must declare exactly one of parent_image or from", name)
		}
		if hasFrom {
			if _, ok := bases[b.From]; !ok {
				return fmt.Errorf("mcp.bases.%s: from references unknown base %q", name, b.From)
			}
		}
	}
	// Cycle detection via DFS with white/gray/black coloring.
	const (
		white = 0
		gray  = 1
		black = 2
	)
	colour := make(map[string]int, len(bases))
	var visit func(name, path string) error
	visit = func(name, path string) error {
		switch colour[name] {
		case gray:
			return fmt.Errorf("mcp.bases: cycle detected: %s -> %s", path, name)
		case black:
			return nil
		}
		colour[name] = gray
		if from := bases[name].From; from != "" {
			if err := visit(from, path+" -> "+name); err != nil {
				return err
			}
		}
		colour[name] = black
		return nil
	}
	for name := range bases {
		if err := visit(name, ""); err != nil {
			return err
		}
	}
	return nil
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

// MCPStateFile returns the resolved path to the MCP state file.
func (c *Config) MCPStateFile() string {
	if c.MCP.StateFile != "" {
		return expandHome(c.MCP.StateFile)
	}
	return filepath.Join(mcpCacheDir(), "mcp-state.json")
}

// MCPPIDFile returns the resolved path to the MCP pidfile.
func (c *Config) MCPPIDFile() string {
	if c.MCP.PIDFile != "" {
		return expandHome(c.MCP.PIDFile)
	}
	return filepath.Join(mcpCacheDir(), "mcp.pid")
}

func mcpCacheDir() string {
	if dir := os.Getenv("XDG_CACHE_HOME"); dir != "" {
		return filepath.Join(dir, "pixels")
	}
	dir, _ := os.UserCacheDir()
	return filepath.Join(dir, "pixels")
}

// KnownHostsPath returns the path to the pixels-managed SSH known_hosts file.
func KnownHostsPath() string {
	dir := filepath.Dir(configPath())
	return filepath.Join(dir, "known_hosts")
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
