package cmd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	mcppkg "github.com/deevus/pixels/internal/mcp"
	"github.com/spf13/cobra"
)

var (
	mcpListenAddr string
	mcpStateFile  string
	mcpPIDFile    string
	mcpVerbose    bool
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run the pixels MCP server (streamable-HTTP)",
	RunE:  runMCP,
}

func init() {
	mcpCmd.Flags().StringVar(&mcpListenAddr, "listen-addr", "", "override [mcp].listen_addr")
	mcpCmd.Flags().StringVar(&mcpStateFile, "state-file", "", "override [mcp].state_file")
	mcpCmd.Flags().StringVar(&mcpPIDFile, "pid-file", "", "override [mcp].pid_file")
	mcpCmd.Flags().BoolVarP(&mcpVerbose, "verbose", "v", false, "log at debug level (tool entry/exit, backend calls)")
	rootCmd.AddCommand(mcpCmd)
}

func runMCP(cmd *cobra.Command, args []string) error {
	listenAddr := pickStr(mcpListenAddr, cfg.MCP.ListenAddr)
	stateFile := pickStr(mcpStateFile, cfg.MCPStateFile())
	pidFile := pickStr(mcpPIDFile, cfg.MCPPIDFile())

	pf, err := mcppkg.AcquirePIDFile(pidFile)
	if err != nil {
		return err
	}
	defer pf.Release()

	state, err := mcppkg.LoadState(stateFile)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	sb, err := openSandbox()
	if err != nil {
		return err
	}
	defer sb.Close()

	idle, err := time.ParseDuration(cfg.MCP.IdleStopAfter)
	if err != nil {
		return fmt.Errorf("idle_stop_after: %w", err)
	}
	hard, err := time.ParseDuration(cfg.MCP.HardDestroyAfter)
	if err != nil {
		return fmt.Errorf("hard_destroy_after: %w", err)
	}
	reapInterval, err := time.ParseDuration(cfg.MCP.ReapInterval)
	if err != nil {
		return fmt.Errorf("reap_interval: %w", err)
	}
	execMax, err := time.ParseDuration(cfg.MCP.ExecTimeoutMax)
	if err != nil {
		return fmt.Errorf("exec_timeout_max: %w", err)
	}

	defaultImg := cfg.MCP.DefaultImage
	if defaultImg == "" {
		defaultImg = cfg.Defaults.Image
	}

	log := mcppkg.NewLogger(os.Stderr, mcpVerbose)
	state.SetLogger(log)
	locks := &mcppkg.SandboxLocks{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	buildLockDir := filepath.Dir(stateFile)
	builder := &mcppkg.Builder{
		FailureTTL: 10 * time.Minute,
	}
	builder.DoBuild = func(buildCtx context.Context, name string) error {
		bl, err := mcppkg.AcquireBuildLock(buildLockDir, name)
		if err != nil {
			return err
		}
		defer bl.Release()
		baseCfg, ok := cfg.MCP.Bases[name]
		if !ok {
			return fmt.Errorf("base %q not declared", name)
		}
		return mcppkg.BuildBase(buildCtx, sb, cfg, baseCfg, name, os.Stderr)
	}

	mux, tools := mcppkg.NewServer(mcppkg.ServerOpts{
		State:          state,
		Backend:        sb,
		Prefix:         cfg.MCP.Prefix,
		DefaultImage:   defaultImg,
		ExecTimeoutMax: execMax,
		Log:            log,
		Locks:          locks,
		DaemonCtx:      ctx,
		Cfg:            cfg,
		Builder:        builder,
		BuildLockDir:   buildLockDir,
	}, cfg.MCP.EndpointPath)

	reaper := &mcppkg.Reaper{
		State:            state,
		Backend:          sb,
		Locks:            locks,
		IdleStopAfter:    idle,
		HardDestroyAfter: hard,
		Log:              log,
	}
	reaper.Tick(ctx) // immediate startup pass
	go reaper.Run(ctx, reapInterval)

	srv := &http.Server{Addr: listenAddr, Handler: mux}

	if !isLoopback(listenAddr) {
		fmt.Fprintf(os.Stderr, "pixels mcp: WARNING bound non-loopback address %q with no auth\n", listenAddr)
	}

	go func() {
		fmt.Fprintf(os.Stderr, "pixels mcp: listening on http://%s%s\n", listenAddr, cfg.MCP.EndpointPath)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "pixels mcp: listen: %v\n", err)
			cancel()
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-stop:
	case <-ctx.Done():
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	cancel()

	// Wait up to 30s for in-flight provisioning goroutines to finish, so their
	// final State writes (MarkRunning / MarkFailed / SetIP) make it to disk.
	done := make(chan struct{})
	go func() {
		tools.WaitProvisioning()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		log.Warn("daemon shutdown: provisioning goroutines still in flight after 30s; final state may be incomplete")
	}

	_ = state.Save()
	return nil
}

func pickStr(override, fallback string) string {
	if override != "" {
		return override
	}
	return fallback
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
