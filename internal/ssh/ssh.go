package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sort"
	"time"

	"github.com/deevus/pixels/internal/retry"
)

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
// If env is non-nil, the entries are forwarded via SSH SetEnv.
func Exec(ctx context.Context, host, user, keyPath string, command []string, env map[string]string) (int, error) {
	args := append(sshArgs(host, user, keyPath, env), command...)
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

// Output runs a command on the remote host via SSH and returns its stdout.
func Output(ctx context.Context, host, user, keyPath string, command []string) ([]byte, error) {
	args := append(sshArgs(host, user, keyPath, nil), command...)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stderr = os.Stderr
	return cmd.Output()
}

// WaitProvisioned polls the remote host until /root/.devtools-provisioned exists.
func WaitProvisioned(ctx context.Context, host, user, keyPath string, timeout time.Duration) error {
	return retry.Poll(ctx, 2*time.Second, timeout, func(ctx context.Context) (bool, error) {
		code, err := Exec(ctx, host, user, keyPath, []string{"sudo", "test", "-f", "/root/.devtools-provisioned"}, nil)
		return err == nil && code == 0, nil
	})
}

// TestAuth runs a quick SSH connection test (ssh ... true) to verify
// key-based authentication works. Returns nil on success.
func TestAuth(ctx context.Context, host, user, keyPath string) error {
	args := append(sshArgs(host, user, keyPath, nil), "true")
	cmd := exec.CommandContext(ctx, "ssh", args...)
	return cmd.Run()
}

func sshArgs(host, user, keyPath string, env map[string]string) []string {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=" + os.DevNull,
		"-o", "PasswordAuthentication=no",
		"-o", "LogLevel=ERROR",
	}
	if keyPath != "" {
		args = append(args, "-i", keyPath)
	}

	// Forward env vars via SSH protocol (requires AcceptEnv on server).
	if len(env) > 0 {
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			args = append(args, "-o", fmt.Sprintf("SetEnv=%s=%s", k, env[k]))
		}
	}

	args = append(args, user+"@"+host)
	return args
}
