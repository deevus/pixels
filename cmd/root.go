package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/config"
	"github.com/deevus/pixels/sandbox"

	// Register the TrueNAS sandbox backend.
	_ "github.com/deevus/pixels/sandbox/truenas"
)

var (
	cfg     *config.Config
	verbose bool
)

var rootCmd = &cobra.Command{
	Use:   "pixels",
	Short: "Disposable Linux containers on TrueNAS",
	Long:  "Create, checkpoint, and restore disposable Incus containers on TrueNAS.",
	SilenceUsage: true,
	PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		var err error
		cfg, err = config.Load()
		if err != nil {
			return err
		}
		if v, _ := cmd.Flags().GetString("host"); v != "" {
			cfg.TrueNAS.Host = v
		}
		if v, _ := cmd.Flags().GetString("api-key"); v != "" {
			cfg.TrueNAS.APIKey = v
		}
		if v, _ := cmd.Flags().GetString("username"); v != "" {
			cfg.TrueNAS.Username = v
		}
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")
	rootCmd.PersistentFlags().String("host", "", "TrueNAS host (overrides config)")
	rootCmd.PersistentFlags().String("api-key", "", "TrueNAS API key (overrides config)")
	rootCmd.PersistentFlags().StringP("username", "u", "", "TrueNAS username (overrides config)")
}

func logv(cmd *cobra.Command, format string, a ...any) {
	if verbose {
		fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", a...)
	}
}

// openSandbox constructs a Sandbox from the loaded config.
func openSandbox() (sandbox.Sandbox, error) {
	m := map[string]string{
		"host":    cfg.TrueNAS.Host,
		"api_key": cfg.TrueNAS.APIKey,
	}
	if cfg.TrueNAS.Port != 0 {
		m["port"] = strconv.Itoa(cfg.TrueNAS.Port)
	}
	if cfg.TrueNAS.Username != "" {
		m["username"] = cfg.TrueNAS.Username
	}
	if cfg.TrueNAS.InsecureSkipVerify != nil {
		m["insecure"] = strconv.FormatBool(*cfg.TrueNAS.InsecureSkipVerify)
	}
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
	if cfg.SSH.User != "" {
		m["ssh_user"] = cfg.SSH.User
	}
	if cfg.SSH.Key != "" {
		m["ssh_key"] = cfg.SSH.Key
	}
	if cfg.Checkpoint.DatasetPrefix != "" {
		m["dataset_prefix"] = cfg.Checkpoint.DatasetPrefix
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
	return sandbox.Open("truenas", m)
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
