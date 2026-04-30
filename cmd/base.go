package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"time"

	"github.com/briandowns/spinner"
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
		// A base is "ready" iff the initial checkpoint exists — that's what
		// sandboxes clone from. A bare container with no checkpoint means
		// the build crashed mid-way (e.g. setup script failure, daemon
		// killed before snapshot) and the container needs to be deleted
		// and rebuilt.
		_, err := sb.Get(context.Background(), container)
		if err != nil {
			status = "missing"
		} else if latest, ok, lerr := mcppkg.LatestCheckpointFor(context.Background(), sb, container); lerr == nil && ok {
			status = "ready"
			lastChk = latest.CreatedAt.UTC().Format("2006-01-02 15:04:05Z")
		} else {
			status = "incomplete"
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

	stderr := cmd.ErrOrStderr()

	// Spinner for non-verbose mode — shows current phase on stderr.
	var spin *spinner.Spinner
	if !verbose {
		spin = spinner.New(spinner.CharSets[14], 100*time.Millisecond, spinner.WithWriter(stderr))
	}
	setStatus := func(msg string) {
		if spin != nil {
			spin.Suffix = "  " + msg
			if !spin.Active() {
				spin.Start()
			}
		}
	}
	stopSpinner := func() {
		if spin != nil && spin.Active() {
			spin.Stop()
		}
	}
	defer stopSpinner()

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

		// Cascade-build header (visible in both modes).
		stopSpinner()
		fmt.Fprintf(stderr, "==> Building base %s...\n", baseName)

		// Choose Out writer + Progress callback per verbose mode.
		var out io.Writer
		var captured *bytes.Buffer
		var progress func(string)
		if verbose {
			out = stderr
			progress = func(phase string) { fmt.Fprintf(stderr, "==> %s\n", phase) }
		} else {
			captured = &bytes.Buffer{}
			out = captured
			progress = setStatus
		}

		start := time.Now()
		err = mcppkg.BuildBase(context.Background(), sb, cfg, baseName, baseCfg, mcppkg.BuildBaseOpts{
			Out:      out,
			Progress: progress,
		})
		stopSpinner()
		if err != nil {
			// In non-verbose mode, dump captured script output before the error so the user
			// can see why the script failed.
			if captured != nil && captured.Len() > 0 {
				fmt.Fprintln(stderr, "--- captured output ---")
				_, _ = io.Copy(stderr, captured)
				fmt.Fprintln(stderr, "--- end output ---")
			}
			return err
		}
		fmt.Fprintf(stderr, "Built %s in %s\n", baseName, time.Since(start).Truncate(100*time.Millisecond))
		return nil
	}

	return mcppkg.BuildChain(context.Background(), cfg, name, exists, build)
}
