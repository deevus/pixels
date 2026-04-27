package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	mcppkg "github.com/deevus/pixels/internal/mcp"
	"github.com/spf13/cobra"
)

var baseCmd = &cobra.Command{
	Use:   "base",
	Short: "Manage pixel bases (templates that sandboxes clone from)",
}

var baseListCmd = &cobra.Command{
	Use:   "list",
	Short: "List declared bases and their status",
	RunE:  runBaseList,
}

var baseBuildCmd = &cobra.Command{
	Use:   "build <name>",
	Short: "Build a base from its setup script (cascade-builds dependencies if missing)",
	Args:  cobra.ExactArgs(1),
	RunE:  runBaseBuild,
}

func init() {
	baseCmd.AddCommand(baseListCmd)
	baseCmd.AddCommand(baseBuildCmd)
	rootCmd.AddCommand(baseCmd)
}

func runBaseList(cmd *cobra.Command, args []string) error {
	if cfg == nil {
		return fmt.Errorf("config not loaded")
	}
	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	w := newTabWriter(cmd)
	defer w.Flush()
	fmt.Fprintln(w, "NAME\tFROM/IMAGE\tSTATUS\tLAST_CHECKPOINT\tDESCRIPTION")

	for name, b := range cfg.MCP.Bases {
		container := mcppkg.BaseName(cfg, name)
		fromOrImage := b.ParentImage
		if b.From != "" {
			fromOrImage = "from:" + b.From
		}

		var status, lastChk string
		if _, err := sb.Get(context.Background(), container); err != nil {
			status = "missing"
		} else {
			status = "ready"
			if latest, ok, err := mcppkg.LatestCheckpointFor(context.Background(), sb, container); err == nil && ok {
				lastChk = latest.CreatedAt.UTC().Format("2006-01-02 15:04:05Z")
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", name, fromOrImage, status, lastChk, b.Description)
	}
	return nil
}

func runBaseBuild(cmd *cobra.Command, args []string) error {
	name := args[0]
	if cfg == nil {
		return fmt.Errorf("config not loaded")
	}
	if _, ok := cfg.MCP.Bases[name]; !ok {
		return fmt.Errorf("base %q not declared in config", name)
	}
	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	exists := func(container string) bool {
		_, err := sb.Get(context.Background(), container)
		return err == nil
	}
	build := func(baseName string) error {
		baseCfg := cfg.MCP.Bases[baseName]
		// File-lock per base name across CLI vs daemon.
		lockDir := filepath.Dir(cfg.MCPStateFile())
		bl, err := mcppkg.AcquireBuildLock(lockDir, baseName)
		if err != nil {
			return err
		}
		defer bl.Release()
		return mcppkg.BuildBase(context.Background(), sb, cfg, baseName, baseCfg, os.Stderr)
	}
	return mcppkg.BuildChain(context.Background(), cfg, name, exists, build)
}
