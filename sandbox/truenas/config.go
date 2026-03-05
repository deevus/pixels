package truenas

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// tnConfig holds parsed backend configuration.
type tnConfig struct {
	host     string
	port     int
	username string
	apiKey   string
	insecure bool

	image  string
	cpu    string
	memory int64 // MiB
	pool   string

	nicType string
	parent  string

	sshUser    string
	sshKey     string
	knownHosts string

	datasetPrefix string

	provision bool
	devtools  bool
	egress    string
	allow     []string
	dns       []string

	env        map[string]string
	envForward map[string]string
}

// parseCfg extracts a tnConfig from a flat key-value map.
func parseCfg(m map[string]string) (*tnConfig, error) {
	c := &tnConfig{
		username:  "root",
		image:     "ubuntu/24.04",
		cpu:       "2",
		memory:    2048,
		pool:      "tank",
		sshUser:   "pixel",
		sshKey:    "~/.ssh/id_ed25519",
		provision: true,
		devtools:  true,
		egress:    "unrestricted",
	}

	if v := m["host"]; v != "" {
		c.host = v
	}
	if v := m["port"]; v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid port %q: %w", v, err)
		}
		c.port = p
	}
	if v := m["username"]; v != "" {
		c.username = v
	}
	if v := m["api_key"]; v != "" {
		c.apiKey = v
	}
	if v := m["insecure"]; v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("invalid insecure %q: %w", v, err)
		}
		c.insecure = b
	}

	if v := m["image"]; v != "" {
		c.image = v
	}
	if v := m["cpu"]; v != "" {
		c.cpu = v
	}
	if v := m["memory"]; v != "" {
		mem, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid memory %q: %w", v, err)
		}
		c.memory = mem
	}
	if v := m["pool"]; v != "" {
		c.pool = v
	}

	if v := m["nic_type"]; v != "" {
		c.nicType = v
	}
	if v := m["parent"]; v != "" {
		c.parent = v
	}

	if v := m["ssh_user"]; v != "" {
		c.sshUser = v
	}
	if v := m["ssh_key"]; v != "" {
		c.sshKey = v
	}
	c.knownHosts = m["ssh_known_hosts"]

	if v := m["dataset_prefix"]; v != "" {
		c.datasetPrefix = v
	}

	if v := m["provision"]; v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("invalid provision %q: %w", v, err)
		}
		c.provision = b
	}
	if v := m["devtools"]; v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("invalid devtools %q: %w", v, err)
		}
		c.devtools = b
	}
	if v := m["egress"]; v != "" {
		switch v {
		case "unrestricted", "agent", "allowlist":
			c.egress = v
		default:
			return nil, fmt.Errorf("invalid egress %q: must be unrestricted, agent, or allowlist", v)
		}
	}
	if v := m["allow"]; v != "" {
		c.allow = strings.Split(v, ",")
	}
	if v := m["dns"]; v != "" {
		c.dns = strings.Split(v, ",")
	}

	// Validate required fields.
	if c.host == "" {
		return nil, fmt.Errorf("host is required")
	}
	if c.apiKey == "" {
		return nil, fmt.Errorf("api_key is required")
	}

	c.sshKey = expandHome(c.sshKey)

	return c, nil
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func boolPtr(v bool) *bool { return &v }
