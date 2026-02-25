package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// WaitReady polls the host's SSH port until it accepts connections or the timeout expires.
func WaitReady(ctx context.Context, host string, timeout time.Duration) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

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
				return nil
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

func sshArgs(host, user, keyPath string) []string {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
	}
	if keyPath != "" {
		args = append(args, "-i", keyPath)
	}
	args = append(args, user+"@"+host)
	return args
}
