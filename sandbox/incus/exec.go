package incus

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lxc/incus/v6/shared/api"
	"golang.org/x/term"

	incusclient "github.com/lxc/incus/v6/client"

	"github.com/deevus/pixels/internal/cache"
	"github.com/deevus/pixels/internal/retry"
	"github.com/deevus/pixels/sandbox"
)

// Run executes a command inside a sandbox instance and returns its exit code.
func (i *Incus) Run(ctx context.Context, name string, opts sandbox.ExecOpts) (int, error) {
	full := prefixed(name)

	env := envSliceToMap(opts.Env)

	interactive := opts.Stdin != nil
	execPost := api.InstanceExecPost{
		Command:     opts.Cmd,
		WaitForWS:   true,
		Interactive: interactive,
		Environment: env,
	}

	if interactive {
		// Set terminal dimensions if stdin is a terminal.
		if f, ok := opts.Stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			w, h, err := term.GetSize(int(f.Fd()))
			if err == nil {
				execPost.Width = w
				execPost.Height = h
			}
		}
	}

	stdin := opts.Stdin
	stdout := opts.Stdout
	stderr := opts.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	dataDone := make(chan bool)
	args := &incusclient.InstanceExecArgs{
		Stdin:    stdin,
		Stdout:   stdout,
		Stderr:   stderr,
		DataDone: dataDone,
	}

	op, err := i.server.ExecInstance(full, execPost, args)
	if err != nil {
		return 1, fmt.Errorf("exec on %s: %w", name, err)
	}

	if err := op.WaitContext(ctx); err != nil {
		return 1, fmt.Errorf("waiting for exec: %w", err)
	}

	// Wait for data transfer to complete.
	<-dataDone

	return exitCodeFromOp(op), nil
}

// Output executes a command and returns its stdout.
func (i *Incus) Output(ctx context.Context, name string, cmd []string) ([]byte, error) {
	full := prefixed(name)

	var stdout bytes.Buffer
	dataDone := make(chan bool)

	args := &incusclient.InstanceExecArgs{
		Stdout:   &stdout,
		Stderr:   io.Discard,
		DataDone: dataDone,
	}

	op, err := i.server.ExecInstance(full, api.InstanceExecPost{
		Command:     cmd,
		WaitForWS:   true,
		Interactive: false,
	}, args)
	if err != nil {
		return nil, fmt.Errorf("exec on %s: %w", name, err)
	}

	if err := op.WaitContext(ctx); err != nil {
		return nil, fmt.Errorf("waiting for exec: %w", err)
	}

	<-dataDone

	rc := exitCodeFromOp(op)
	if rc != 0 {
		return stdout.Bytes(), fmt.Errorf("command exited with code %d", rc)
	}

	return stdout.Bytes(), nil
}

// Console attaches an interactive console session to a container.
func (i *Incus) Console(ctx context.Context, name string, opts sandbox.ConsoleOpts) error {
	full := prefixed(name)

	cmd := opts.RemoteCmd
	if len(cmd) == 0 {
		cmd = []string{"bash", "-l"}
	}

	env := envSliceToMap(opts.Env)

	// Get terminal size.
	var width, height int
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		w, h, err := term.GetSize(fd)
		if err == nil {
			width, height = w, h
		}
	}

	execPost := api.InstanceExecPost{
		Command:     cmd,
		WaitForWS:   true,
		Interactive: true,
		Environment: env,
		Width:       width,
		Height:      height,
	}

	// Set terminal to raw mode.
	var oldState *term.State
	if term.IsTerminal(fd) {
		var err error
		oldState, err = term.MakeRaw(fd)
		if err != nil {
			return fmt.Errorf("setting raw terminal: %w", err)
		}
		defer term.Restore(fd, oldState)
	}

	dataDone := make(chan bool)
	args := &incusclient.InstanceExecArgs{
		Stdin:    os.Stdin,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		DataDone: dataDone,
		Control: func(conn *websocket.Conn) {
			// Handle SIGWINCH for terminal resize.
			ch := make(chan os.Signal, 1)
			signal.Notify(ch, sigWINCH())
			defer signal.Stop(ch)

			for range ch {
				w, h, err := term.GetSize(fd)
				if err != nil {
					continue
				}
				msg := api.InstanceExecControl{
					Command: "window-resize",
					Args: map[string]string{
						"width":  fmt.Sprintf("%d", w),
						"height": fmt.Sprintf("%d", h),
					},
				}
				_ = conn.WriteJSON(msg)
			}
		},
	}

	op, err := i.server.ExecInstance(full, execPost, args)
	if err != nil {
		return fmt.Errorf("exec console on %s: %w", name, err)
	}

	if err := op.WaitContext(ctx); err != nil {
		return fmt.Errorf("waiting for console: %w", err)
	}

	<-dataDone

	rc := exitCodeFromOp(op)
	if rc != 0 {
		return fmt.Errorf("console exited with code %d", rc)
	}

	return nil
}

// Ready waits until the instance is running and reachable.
func (i *Incus) Ready(ctx context.Context, name string, timeout time.Duration) error {
	full := prefixed(name)
	err := retry.Poll(ctx, time.Second, timeout, func(ctx context.Context) (bool, error) {
		state, _, err := i.server.GetInstanceState(full)
		if err != nil {
			return false, nil
		}
		if state.StatusCode != api.Running {
			return false, nil
		}
		// Check for network addresses (indicates agent is responsive).
		addrs := extractIPv4(state)
		if len(addrs) > 0 {
			cache.Put(name, &cache.Entry{IP: addrs[0], Status: "RUNNING"})
		}
		return state.Pid > 0, nil
	})
	if err != nil {
		return fmt.Errorf("waiting for %s to be ready: %w", name, err)
	}
	return nil
}

// exitCodeFromOp extracts the exit code from an exec operation's metadata.
func exitCodeFromOp(op incusclient.Operation) int {
	md := op.Get().Metadata
	if md != nil {
		if rc, ok := md["return"].(float64); ok {
			return int(rc)
		}
	}
	return 0
}

// envSliceToMap converts a slice of "KEY=VALUE" pairs to a map.
func envSliceToMap(env []string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	m := make(map[string]string, len(env))
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok {
			m[k] = v
		}
	}
	return m
}
