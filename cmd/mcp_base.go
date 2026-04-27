package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/deevus/pixels/internal/config"
	mcppkg "github.com/deevus/pixels/internal/mcp"
	"github.com/deevus/pixels/sandbox"
	"github.com/spf13/cobra"
)

func init() {
	mcpCmd.AddCommand(mcpBuildBaseCmd)
	mcpCmd.AddCommand(mcpRebuildBaseCmd)
	mcpCmd.AddCommand(mcpDeleteBaseCmd)
	mcpCmd.AddCommand(mcpListBasesCmd)
}

var mcpBuildBaseCmd = &cobra.Command{
	Use:   "build-base <name>",
	Short: "Build a base pixel from config",
	Args:  cobra.ExactArgs(1),
	RunE:  runBuildBase,
}

var mcpRebuildBaseCmd = &cobra.Command{
	Use:   "rebuild-base <name>",
	Short: "Delete an existing base pixel snapshot and rebuild",
	Args:  cobra.ExactArgs(1),
	RunE:  runRebuildBase,
}

var mcpDeleteBaseCmd = &cobra.Command{
	Use:   "delete-base <name>",
	Short: "Delete a base pixel snapshot",
	Args:  cobra.ExactArgs(1),
	RunE:  runDeleteBase,
}

var mcpListBasesCmd = &cobra.Command{
	Use:   "list-bases",
	Short: "List declared base pixels and their status",
	RunE:  runListBases,
}

func runBuildBase(cmd *cobra.Command, args []string) error {
	name := args[0]
	cfg, sb, lockDir, err := setupBaseCmd()
	if err != nil {
		return err
	}
	defer sb.Close()

	baseCfg, ok := cfg.MCP.Bases[name]
	if !ok {
		return fmt.Errorf("base %q not declared in config", name)
	}

	bl, err := mcppkg.AcquireBuildLock(lockDir, name)
	if err != nil {
		return err
	}
	defer bl.Release()

	return mcppkg.BuildBase(context.Background(), sb, baseCfg, name, os.Stderr)
}

func runRebuildBase(cmd *cobra.Command, args []string) error {
	name := args[0]
	cfg, sb, lockDir, err := setupBaseCmd()
	if err != nil {
		return err
	}
	defer sb.Close()

	baseCfg, ok := cfg.MCP.Bases[name]
	if !ok {
		return fmt.Errorf("base %q not declared in config", name)
	}

	bl, err := mcppkg.AcquireBuildLock(lockDir, name)
	if err != nil {
		return err
	}
	defer bl.Release()

	// Best-effort: delete existing snapshot if present.
	// For v1, backends don't have a standalone-snapshot-delete API that works
	// across containers. Document the limitation.
	fmt.Fprintf(os.Stderr, "WARN: existing snapshot not auto-deleted; run `pixels mcp delete-base %s` first if it exists\n", name)
	return mcppkg.BuildBase(context.Background(), sb, baseCfg, name, os.Stderr)
}

func runDeleteBase(cmd *cobra.Command, args []string) error {
	name := args[0]
	_, sb, _, err := setupBaseCmd()
	if err != nil {
		return err
	}
	defer sb.Close()

	// Base snapshots live on the build temp containers; there's no standalone
	// API to delete them without knowing which container owns them.
	fmt.Fprintf(os.Stderr, "delete-base for %s: not implemented in v1; manually use `incus delete <snapshot>` or delete the px-build-* container\n", name)
	return nil
}

func runListBases(cmd *cobra.Command, args []string) error {
	cfg, _, _, err := setupBaseCmd()
	if err != nil {
		return err
	}
	w := newTabWriter(cmd)
	defer w.Flush()
	fmt.Fprintln(w, "NAME\tPARENT\tDESCRIPTION")
	for name, b := range cfg.MCP.Bases {
		fmt.Fprintf(w, "%s\t%s\t%s\n", name, b.ParentImage, b.Description)
	}
	return nil
}

func setupBaseCmd() (*config.Config, sandbox.Sandbox, string, error) {
	if cfg == nil {
		return nil, nil, "", fmt.Errorf("config not loaded")
	}
	sb, err := openSandbox()
	if err != nil {
		return nil, nil, "", err
	}
	stateFile := cfg.MCPStateFile()
	lockDir := filepath.Dir(stateFile)
	return cfg, sb, lockDir, nil
}
