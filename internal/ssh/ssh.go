package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"
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

// Console replaces the current process with an interactive SSH session.
func Console(host, user, keyPath string) error {
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("ssh binary not found: %w", err)
	}
	args := append([]string{"ssh"}, sshArgs(host, user, keyPath)...)
	return syscall.Exec(sshBin, args, os.Environ())
}

// Exec runs a command on the remote host via SSH and returns its exit code.
func Exec(ctx context.Context, host, user, keyPath string, command []string) (int, error) {
	args := append(sshArgs(host, user, keyPath), command...)
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

// WaitProvisioned polls the remote host until /root/.devtools-provisioned exists.
func WaitProvisioned(ctx context.Context, host, user, keyPath string, timeout time.Duration) error {
	return pollUntil(ctx, 2*time.Second, timeout, func(ctx context.Context) bool {
		code, err := Exec(ctx, host, user, keyPath, []string{"sudo", "test", "-f", "/root/.devtools-provisioned"})
		return err == nil && code == 0
	})
}

// pollUntil calls checkFn at the given interval until it returns true or the
// timeout/context expires.
func pollUntil(ctx context.Context, interval, timeout time.Duration, checkFn func(ctx context.Context) bool) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timed out after %s", timeout)
		case <-ticker.C:
			if checkFn(ctx) {
				return nil
			}
		}
	}
}

// TestAuth runs a quick SSH connection test (ssh ... true) to verify
// key-based authentication works. Returns nil on success.
func TestAuth(ctx context.Context, host, user, keyPath string) error {
	args := append(sshArgs(host, user, keyPath), "true")
	cmd := exec.CommandContext(ctx, "ssh", args...)
	return cmd.Run()
}

func sshArgs(host, user, keyPath string) []string {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "PasswordAuthentication=no",
		"-o", "LogLevel=ERROR",
	}
	if keyPath != "" {
		args = append(args, "-i", keyPath)
	}
	args = append(args, user+"@"+host)
	return args
}
