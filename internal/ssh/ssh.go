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
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ConnConfig holds the parameters for an SSH connection.
// Use NewConnConfig to construct — it ensures secure defaults.
type ConnConfig struct {
	Host           string
	User           string
	KeyPath        string
	Env            map[string]string // optional, for SetEnv forwarding
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
	return cmd.Output()
}

// OutputQuiet runs a command on the remote host via SSH and returns its stdout,
// discarding stderr. Use this when parsing command output programmatically.
func OutputQuiet(ctx context.Context, cc ConnConfig, command []string) ([]byte, error) {
	args := append(Args(cc), command...)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	return cmd.Output()
}

// TestAuth runs a quick SSH connection test (ssh ... true) to verify
// key-based authentication works. Returns nil on success.
func TestAuth(ctx context.Context, cc ConnConfig) error {
	args := append(Args(cc), "true")
	cmd := exec.CommandContext(ctx, "ssh", args...)
	return cmd.Run()
}

// Args builds the SSH command-line arguments for the given connection config.
// It is exported for use by callers that need to construct custom exec.Cmd
// with non-standard Stdin/Stdout/Stderr (e.g. sandbox backends).
func Args(cc ConnConfig) []string {
	knownHosts := cc.KnownHostsPath
	if knownHosts == "" {
		knownHosts = defaultKnownHostsPath()
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
	// All vars must be in a single SetEnv directive (multiple -o SetEnv
	// flags don't stack in OpenSSH — only the first takes effect).
	if len(cc.Env) > 0 {
		keys := make([]string, 0, len(cc.Env))
		for k := range cc.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		var setenv strings.Builder
		setenv.WriteString("SetEnv=")
		for i, k := range keys {
			if i > 0 {
				setenv.WriteByte(' ')
			}
			fmt.Fprintf(&setenv, "%s=%s", k, cc.Env[k])
		}
		args = append(args, "-o", setenv.String())
	}

	args = append(args, cc.User+"@"+cc.Host)
	return args
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

// defaultKnownHostsPath returns the default known_hosts path in the pixels
// config directory. This ensures Args() is always secure even if
// KnownHostsPath is not explicitly set on ConnConfig.
func defaultKnownHostsPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "pixels", "known_hosts")
	}
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "pixels", "known_hosts")
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
