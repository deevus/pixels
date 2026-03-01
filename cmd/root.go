package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/deevus/pixels/internal/config"
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

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
