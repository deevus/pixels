package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/config"
	"github.com/deevus/pixels/sandbox"

	// Register sandbox backends.
	_ "github.com/deevus/pixels/sandbox/incus"
	_ "github.com/deevus/pixels/sandbox/truenas"
)

var (
	cfg     *config.Config
	verbose bool
)

var rootCmd = &cobra.Command{
	Use:   "pixels",
	Short: "Disposable Linux containers via Incus",
	Long:  "Create, checkpoint, and restore disposable Incus containers.",
	SilenceUsage: true,
	PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
		var err error
		cfg, err = config.Load()
		if err != nil {
			return err
		}
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")
}

func logv(cmd *cobra.Command, format string, a ...any) {
	if verbose {
		fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", a...)
	}
}

// sandboxConfig returns the config map for constructing a sandbox.
// It populates both shared keys and backend-specific keys based on cfg.Backend.
func sandboxConfig() map[string]string {
	m := map[string]string{}

	// Shared keys used by all backends.
	if cfg.Defaults.Image != "" {
		m["image"] = cfg.Defaults.Image
	}
	if cfg.Defaults.CPU != "" {
		m["cpu"] = cfg.Defaults.CPU
	}
	if cfg.Defaults.Memory != 0 {
		m["memory"] = strconv.FormatInt(cfg.Defaults.Memory, 10)
	}
	if cfg.Defaults.Pool != "" {
		m["pool"] = cfg.Defaults.Pool
	}
	if cfg.Defaults.NICType != "" {
		m["nic_type"] = cfg.Defaults.NICType
	}
	if cfg.Defaults.Parent != "" {
		m["parent"] = cfg.Defaults.Parent
	}
	if cfg.Defaults.Network != "" {
		m["network"] = cfg.Defaults.Network
	}
	if cfg.SSH.User != "" {
		m["ssh_user"] = cfg.SSH.User
	}
	if cfg.SSH.Key != "" {
		m["ssh_key"] = cfg.SSH.Key
	}
	if cfg.SSH.StrictHostKeysEnabled() {
		m["ssh_known_hosts"] = config.KnownHostsPath()
	}
	m["provision"] = strconv.FormatBool(cfg.Provision.IsEnabled())
	m["devtools"] = strconv.FormatBool(cfg.Provision.DevToolsEnabled())
	if cfg.Network.Egress != "" {
		m["egress"] = cfg.Network.Egress
	}
	if len(cfg.Network.Allow) > 0 {
		m["allow"] = strings.Join(cfg.Network.Allow, ",")
	}
	if len(cfg.Defaults.DNS) > 0 {
		m["dns"] = strings.Join(cfg.Defaults.DNS, ",")
	}

	// Backend-specific keys.
	switch cfg.Backend {
	case "truenas":
		m["host"] = cfg.TrueNAS.Host
		m["api_key"] = cfg.TrueNAS.APIKey
		if cfg.TrueNAS.Port != 0 {
			m["port"] = strconv.Itoa(cfg.TrueNAS.Port)
		}
		if cfg.TrueNAS.Username != "" {
			m["username"] = cfg.TrueNAS.Username
		}
		if cfg.TrueNAS.InsecureSkipVerify != nil {
			m["insecure"] = strconv.FormatBool(*cfg.TrueNAS.InsecureSkipVerify)
		}
		if cfg.Checkpoint.DatasetPrefix != "" {
			m["dataset_prefix"] = cfg.Checkpoint.DatasetPrefix
		}
	case "incus":
		if cfg.Incus.Socket != "" {
			m["socket"] = cfg.Incus.Socket
		}
		if cfg.Incus.Remote != "" {
			m["remote"] = cfg.Incus.Remote
		}
		if cfg.Incus.ClientCert != "" {
			m["client_cert"] = cfg.Incus.ClientCert
		}
		if cfg.Incus.ClientKey != "" {
			m["client_key"] = cfg.Incus.ClientKey
		}
		if cfg.Incus.ServerCert != "" {
			m["server_cert"] = cfg.Incus.ServerCert
		}
		if cfg.Incus.Project != "" {
			m["project"] = cfg.Incus.Project
		}
	}

	return m
}

// openSandbox constructs a Sandbox from the loaded config.
func openSandbox() (sandbox.Sandbox, error) {
	return sandbox.Open(cfg.Backend, sandboxConfig())
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
