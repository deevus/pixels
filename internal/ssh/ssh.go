package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/deevus/pixels/internal/config"
)

// ConnConfig holds the parameters for an SSH connection.
// Use NewConnConfig to construct — it ensures secure defaults.
type ConnConfig struct {
	Host           string
	User           string
	KeyPath        string
	Env            map[string]string // optional, for SendEnv forwarding
	KnownHostsPath string            // path to known_hosts file for accept-new verification
}

// NewConnConfig creates a ConnConfig with the given parameters.
func NewConnConfig(host, user, keyPath, knownHostsPath string) ConnConfig {
	return ConnConfig{
		Host:           host,
		User:           user,
		KeyPath:        keyPath,
		KnownHostsPath: knownHostsPath,
	}
}

// WaitReady polls the host's SSH port until it accepts connections or the timeout expires.
// If log is non-nil, progress is written every 5 seconds.
func WaitReady(ctx context.Context, host string, timeout time.Duration, log io.Writer) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	start := time.Now()
	lastLog := start
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("ssh not ready on %s after %s", host, timeout)
		case <-ticker.C:
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, "22"), 2*time.Second)
			if err == nil {
				conn.Close()
				if log != nil {
					fmt.Fprintf(log, "SSH ready on %s (%s)\n", host, time.Since(start).Truncate(100*time.Millisecond))
				}
				return nil
			}
			if log != nil && time.Since(lastLog) >= 5*time.Second {
				fmt.Fprintf(log, "SSH: waiting for %s (%s elapsed)...\n", host, time.Since(start).Truncate(time.Second))
				lastLog = time.Now()
			}
		}
	}
}

// Exec runs a command on the remote host via SSH and returns its exit code.
func Exec(ctx context.Context, cc ConnConfig, command []string) (int, error) {
	args := append(Args(cc), command...)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	applyEnv(cmd, cc)

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}

// ExecQuiet runs a non-interactive command on the remote host via SSH and
// returns its exit code. Unlike Exec, it does not attach stdin/stdout/stderr.
func ExecQuiet(ctx context.Context, cc ConnConfig, command []string) (int, error) {
	args := append(Args(cc), command...)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	applyEnv(cmd, cc)

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}

// Output runs a command on the remote host via SSH and returns its stdout.
func Output(ctx context.Context, cc ConnConfig, command []string) ([]byte, error) {
	args := append(Args(cc), command...)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stderr = os.Stderr
	applyEnv(cmd, cc)
	return cmd.Output()
}

// OutputQuiet runs a command on the remote host via SSH and returns its stdout,
// discarding stderr. Use this when parsing command output programmatically.
func OutputQuiet(ctx context.Context, cc ConnConfig, command []string) ([]byte, error) {
	args := append(Args(cc), command...)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	applyEnv(cmd, cc)
	return cmd.Output()
}

// TestAuth runs a quick SSH connection test (ssh ... true) to verify
// key-based authentication works. Returns nil on success.
func TestAuth(ctx context.Context, cc ConnConfig) error {
	args := append(Args(cc), "true")
	cmd := exec.CommandContext(ctx, "ssh", args...)
	applyEnv(cmd, cc)
	return cmd.Run()
}

// applyEnv sets cmd.Env if the ConnConfig has env vars to forward via SendEnv.
func applyEnv(cmd *exec.Cmd, cc ConnConfig) {
	if len(cc.Env) > 0 {
		cmd.Env = EnvWithOverrides(os.Environ(), cc.Env)
	}
}

// Args builds the SSH command-line arguments for the given connection config.
// It is exported for use by callers that need to construct custom exec.Cmd
// with non-standard Stdin/Stdout/Stderr (e.g. sandbox backends).
func Args(cc ConnConfig) []string {
	knownHosts := cc.KnownHostsPath
	if knownHosts == "" {
		knownHosts = config.KnownHostsPath()
	}
	args := []string{
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + knownHosts,
		"-o", "PasswordAuthentication=no",
		"-o", "LogLevel=ERROR",
	}
	if cc.KeyPath != "" {
		args = append(args, "-i", cc.KeyPath)
	}

	// Forward env vars via SSH protocol (requires AcceptEnv on server).
	// SendEnv tells the client to read values from its own process
	// environment — only key names appear in argv, not secret values.
	if len(cc.Env) > 0 {
		keys := make([]string, 0, len(cc.Env))
		for k := range cc.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		args = append(args, "-o", "SendEnv="+strings.Join(keys, " "))
	}

	args = append(args, cc.User+"@"+cc.Host)
	return args
}

// EnvWithOverrides returns a copy of base with entries from overrides
// added or replaced. Used to inject env vars into the SSH process
// environment so that SendEnv can forward them.
func EnvWithOverrides(base []string, overrides map[string]string) []string {
	env := make([]string, len(base))
	copy(env, base)
	for k, v := range overrides {
		prefix := k + "="
		found := false
		for i, e := range env {
			if strings.HasPrefix(e, prefix) {
				env[i] = prefix + v
				found = true
				break
			}
		}
		if !found {
			env = append(env, prefix+v)
		}
	}
	return env
}

// RemoveKnownHost removes all entries for the given host from the known_hosts
// file. This is used to clean up stale entries when containers are
// created, destroyed, or restored from snapshots. It is a no-op if the
// known_hosts file does not exist or the path is empty.
func RemoveKnownHost(knownHostsPath, host string) error {
	if knownHostsPath == "" || host == "" {
		return nil
	}
	data, err := os.ReadFile(knownHostsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading known_hosts: %w", err)
	}

	prefix := []byte(host + " ")
	var kept []byte
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		if !bytes.HasPrefix(line, prefix) {
			kept = append(kept, line...)
		}
	}

	return os.WriteFile(knownHostsPath, kept, 0o600)
}


// consoleArgs builds SSH arguments for an interactive console session.
// When remoteCmd is non-empty, -t is inserted to force PTY allocation
// and the command is appended after user@host.
func consoleArgs(cc ConnConfig, remoteCmd string) []string {
	if remoteCmd == "" {
		return Args(cc)
	}
	args := Args(cc)
	// Insert -t before user@host (last element).
	userHost := args[len(args)-1]
	args = append(args[:len(args)-1], "-t", userHost, remoteCmd)
	return args
}
