package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	truenas "github.com/deevus/truenas-go"
	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/config"
	"github.com/deevus/pixels/internal/ssh"
	tnc "github.com/deevus/pixels/internal/truenas"
)

const containerPrefix = "px-"

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

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func connectClient(ctx context.Context) (*tnc.Client, error) {
	if cfg.TrueNAS.Host == "" {
		return nil, fmt.Errorf("TrueNAS host not configured — set truenas.host in config or use --host")
	}
	if cfg.TrueNAS.APIKey == "" {
		return nil, fmt.Errorf("TrueNAS API key not configured — set truenas.api_key in config or use --api-key")
	}
	return tnc.Connect(ctx, cfg)
}

func containerName(name string) string {
	return containerPrefix + name
}

func displayName(name string) string {
	return strings.TrimPrefix(name, containerPrefix)
}

func resolveIP(instance *truenas.VirtInstance) string {
	for _, a := range instance.Aliases {
		if a.Type == "INET" || a.Type == "ipv4" {
			return a.Address
		}
	}
	return ""
}

func newTabWriter(cmd *cobra.Command) *tabwriter.Writer {
	return tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
}

// readSSHPubKey reads the SSH public key from the path configured in ssh.key.
// It derives the .pub path from the private key path.
func readSSHPubKey() (string, error) {
	keyPath := cfg.SSH.Key
	if keyPath == "" {
		return "", nil
	}
	pubPath := keyPath + ".pub"
	data, err := os.ReadFile(pubPath)
	if err != nil {
		return "", fmt.Errorf("reading SSH public key %s: %w", pubPath, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// ensureSSHAuth tests key auth and, if it fails, writes the current machine's
// SSH public key to the container's authorized_keys via TrueNAS.
func ensureSSHAuth(cmd *cobra.Command, ctx context.Context, ip, name string) error {
	if err := ssh.TestAuth(ctx, ip, cfg.SSH.User, cfg.SSH.Key); err == nil {
		return nil
	}

	pubKey, err := readSSHPubKey()
	if err != nil {
		return err
	}
	if pubKey == "" {
		return fmt.Errorf("SSH key auth failed and no public key configured")
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "SSH key not authorized, updating...\n")

	client, err := connectClient(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	return client.WriteAuthorizedKey(ctx, containerName(name), pubKey)
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
