package mcp

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/deevus/pixels/internal/config"
	"github.com/deevus/pixels/sandbox"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerOpts bundles construction parameters.
type ServerOpts struct {
	State          *State
	Backend        sandbox.Sandbox
	Prefix         string
	DefaultImage   string
	ExecTimeoutMax time.Duration
	Log            *slog.Logger
	Locks          *SandboxLocks // shared with Reaper; constructed by caller
	DaemonCtx      context.Context // outlives any single request; provisioning goroutine inherits this
	Cfg            *config.Config
	Builder        *Builder
	BuildLockDir   string
}

// NewServer wires the MCP tool surface and returns an HTTP handler ready to mount.
// The second return value (*Tools) is a test affordance — it gives tests access
// to the handler functions directly without an HTTP round-trip.
func NewServer(opts ServerOpts, endpointPath string) (http.Handler, *Tools) {
	log := opts.Log
	if log == nil {
		log = NopLogger()
	}
	locks := opts.Locks
	if locks == nil {
		locks = &SandboxLocks{}
	}
	tools := &Tools{
		State:          opts.State,
		Backend:        opts.Backend,
		Prefix:         opts.Prefix,
		DefaultImage:   opts.DefaultImage,
		ExecTimeoutMax: opts.ExecTimeoutMax,
		Log:            log,
		Locks:          locks,
	}

	tools.DaemonCtx = opts.DaemonCtx
	tools.Cfg = opts.Cfg
	tools.Builder = opts.Builder
	tools.BuildLockDir = opts.BuildLockDir

	srv := sdk.NewServer(&sdk.Implementation{Name: "pixels-mcp", Version: "0.1.0"}, nil)

	addTool(srv, "create_sandbox", "Create a new ephemeral sandbox container.", tools.CreateSandbox)
	addTool(srv, "destroy_sandbox", "Destroy a sandbox and its filesystem.", tools.DestroySandbox)
	addTool(srv, "start_sandbox", "Start (resume) a stopped sandbox.", tools.StartSandbox)
	addTool(srv, "stop_sandbox", "Stop (pause) a running sandbox.", tools.StopSandbox)
	addTool(srv, "list_sandboxes", "List all tracked sandboxes.", tools.ListSandboxes)
	addTool(srv, "exec", "Run a command inside a sandbox.", tools.Exec)
	addTool(srv, "write_file", "Write a file inside a sandbox (create or full overwrite).", tools.WriteFile)
	addTool(srv, "read_file", "Read a file from a sandbox, optionally truncated.", tools.ReadFile)
	addTool(srv, "list_files", "List files inside a sandbox path.", tools.ListFiles)
	addTool(srv, "edit_file", "Replace one occurrence of old_string with new_string in a file. Pass replace_all=true to replace every occurrence.", tools.EditFile)
	addTool(srv, "delete_file", "Delete a single file from a sandbox.", tools.DeleteFile)

	handler := sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server { return srv }, nil)
	mux := http.NewServeMux()
	mux.Handle(endpointPath, handler)
	return mux, tools
}

// addTool registers an MCP tool from a typed handler function.
func addTool[I, O any](srv *sdk.Server, name, desc string, fn func(context.Context, I) (O, error)) {
	sdk.AddTool(srv, &sdk.Tool{
		Name:        name,
		Description: desc,
	}, adapt(fn))
}

// adapt converts a simple (ctx, In) → (Out, error) function into the SDK's
// handler shape: (ctx, *CallToolRequest, In) → (*CallToolResult, Out, error).
func adapt[I, O any](fn func(context.Context, I) (O, error)) func(context.Context, *sdk.CallToolRequest, I) (*sdk.CallToolResult, O, error) {
	return func(ctx context.Context, _ *sdk.CallToolRequest, in I) (*sdk.CallToolResult, O, error) {
		out, err := fn(ctx, in)
		return nil, out, err
	}
}
