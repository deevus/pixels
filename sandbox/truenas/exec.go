package truenas

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/deevus/pixels/internal/ssh"
	"github.com/deevus/pixels/sandbox"
)

// Run executes a command inside a sandbox instance. If ExecOpts provides
// custom Stdin/Stdout/Stderr, it builds a custom exec.Cmd using ssh.Args().
// Otherwise it delegates to ssh.Exec.
func (t *TrueNAS) Run(ctx context.Context, name string, opts sandbox.ExecOpts) (int, error) {
	if err := t.ensureRunning(ctx, name); err != nil {
		return 1, err
	}

	user := t.cfg.sshUser
	if opts.Root {
		user = "root"
	}

	cc := ssh.ConnConfig{
		Host:           prefixed(name),
		User:           user,
		KeyPath:        t.cfg.sshKey,
		Env:            envToMap(opts.Env),
		KnownHostsFile: t.cfg.knownHosts,
	}

	hasCustomIO := opts.Stdin != nil || opts.Stdout != nil || opts.Stderr != nil
	if hasCustomIO {
		args := append(ssh.Args(cc), opts.Cmd...)
		cmd := exec.CommandContext(ctx, "ssh", args...)
		cmd.Stdin = opts.Stdin
		cmd.Stdout = opts.Stdout
		cmd.Stderr = opts.Stderr

		if err := cmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return exitErr.ExitCode(), nil
			}
			return 1, err
		}
		return 0, nil
	}

	return ssh.Exec(ctx, cc, opts.Cmd)
}

// Output executes a command and returns its combined stdout.
func (t *TrueNAS) Output(ctx context.Context, name string, cmd []string) ([]byte, error) {
	if err := t.ensureRunning(ctx, name); err != nil {
		return nil, err
	}
	cc := ssh.ConnConfig{
		Host:           prefixed(name),
		User:           t.cfg.sshUser,
		KeyPath:        t.cfg.sshKey,
		KnownHostsFile: t.cfg.knownHosts,
	}
	return t.ssh.OutputQuiet(ctx, cc, cmd)
}

// Console attaches an interactive console session.
func (t *TrueNAS) Console(ctx context.Context, name string, opts sandbox.ConsoleOpts) error {
	if err := t.ensureRunning(ctx, name); err != nil {
		return err
	}
	cc := ssh.ConnConfig{
		Host:           prefixed(name),
		User:           t.cfg.sshUser,
		KeyPath:        t.cfg.sshKey,
		Env:            envToMap(opts.Env),
		KnownHostsFile: t.cfg.knownHosts,
	}
	remoteCmd := strings.Join(opts.RemoteCmd, " ")
	return ssh.Console(cc, remoteCmd)
}

// Ready waits until the instance is reachable via SSH. If key auth fails,
// it pushes the current machine's SSH public key via the TrueNAS file API.
func (t *TrueNAS) Ready(ctx context.Context, name string, timeout time.Duration) error {
	if err := t.ensureRunning(ctx, name); err != nil {
		return err
	}
	host := prefixed(name)
	if err := t.ssh.WaitReady(ctx, host, timeout, nil); err != nil {
		return err
	}

	// Test key auth and push the key if it fails.
	cc := ssh.ConnConfig{
		Host:           host,
		User:           t.cfg.sshUser,
		KeyPath:        t.cfg.sshKey,
		KnownHostsFile: t.cfg.knownHosts,
	}
	if err := ssh.TestAuth(ctx, cc); err != nil {
		pubKey := readSSHPubKey(t.cfg.sshKey)
		if pubKey == "" {
			return fmt.Errorf("SSH key auth failed and no public key at %s.pub", t.cfg.sshKey)
		}
		full := "px-" + name
		if writeErr := t.client.WriteAuthorizedKey(ctx, full, pubKey); writeErr != nil {
			return fmt.Errorf("SSH key auth failed; writing key: %w", writeErr)
		}
	}
	return nil
}

// envToMap converts a slice of "KEY=VALUE" pairs to a map.
func envToMap(env []string) map[string]string {
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
