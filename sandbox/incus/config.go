package incus

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/deevus/pixels/sandbox/user"
)

// incusCfg holds parsed backend configuration.
type incusCfg struct {
	socket     string
	remote     string
	clientCert string
	clientKey  string
	serverCert string
	project    string

	image       string
	imageServer string
	cpu         string
	memory      int64 // MiB
	pool        string

	nicType string
	parent  string
	network string

	sshUser string
	sshKey  string
	uid     uint32
	gid     uint32

	provision bool
	devtools  bool
	egress    string
	allow     []string
	dns       []string

	env map[string]string
}

// parseCfg extracts an incusCfg from a flat key-value map.
func parseCfg(m map[string]string) (*incusCfg, error) {
	c := &incusCfg{
		image:       "ubuntu/24.04",
		imageServer: "https://images.linuxcontainers.org",
		cpu:         "2",
		memory:      2048,
		pool:        "default",
		sshUser:     "pixel",
		sshKey:      "~/.ssh/id_ed25519",
		uid:         user.UID,
		gid:         user.GID,
		provision:   true,
		devtools:    true,
		egress:      "unrestricted",
	}

	if v := m["socket"]; v != "" {
		c.socket = v
	}
	if v := m["remote"]; v != "" {
		c.remote = v
	}
	if v := m["client_cert"]; v != "" {
		c.clientCert = v
	}
	if v := m["client_key"]; v != "" {
		c.clientKey = v
	}
	if v := m["server_cert"]; v != "" {
		c.serverCert = v
	}
	if v := m["project"]; v != "" {
		c.project = v
	}

	if v := m["image"]; v != "" {
		c.image = v
	}
	if v := m["image_server"]; v != "" {
		c.imageServer = v
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
	if v := m["network"]; v != "" {
		c.network = v
	}

	if v := m["ssh_user"]; v != "" {
		c.sshUser = v
	}
	if v := m["ssh_key"]; v != "" {
		c.sshKey = v
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
