package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

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

	// Load state from disk to check for build failures.
	state, err := mcppkg.LoadState(cfg.MCPStateFile())
	if err != nil {
		return err
	}

	// Collect and sort base names for deterministic output.
	names := make([]string, 0, len(cfg.MCP.Bases))
	for name := range cfg.MCP.Bases {
		names = append(names, name)
	}
	slices.Sort(names)

	w := newTabWriter(cmd)
	defer w.Flush()
	fmt.Fprintln(w, "NAME\tFROM/IMAGE\tSTATUS\tLAST_CHECKPOINT\tDESCRIPTION")

	for _, name := range names {
		b := cfg.MCP.Bases[name]
		container := mcppkg.BaseName(cfg, name)
		fromOrImage := b.ParentImage
		if b.From != "" {
			fromOrImage = "from:" + b.From
		}

		var status, lastChk string
		// Check container existence.
		_, err := sb.Get(context.Background(), container)
		if err != nil {
			status = "missing"
		} else {
			status = "ready"
			if latest, ok, err := mcppkg.LatestCheckpointFor(context.Background(), sb, container); err == nil && ok {
				lastChk = latest.CreatedAt.UTC().Format("2006-01-02 15:04:05Z")
			}
		}

		// Check for cached build failures in the on-disk state.
		// A failed sandbox indicates a prior build failure.
		if sbState, ok := state.Get(container); ok && sbState.Status == "failed" {
			status = "failed"
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
