package truenas

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/deevus/pixels/internal/retry"
	"github.com/deevus/pixels/internal/ssh"
	"github.com/deevus/pixels/sandbox"
)

// Run executes a command inside a sandbox instance. If ExecOpts provides
// custom Stdin/Stdout/Stderr, it builds a custom exec.Cmd using ssh.Args().
// Otherwise it delegates to ssh.Exec.
func (t *TrueNAS) Run(ctx context.Context, name string, opts sandbox.ExecOpts) (int, error) {
	if _, err := t.ensureRunning(ctx, name); err != nil {
		return 1, err
	}

	user := t.cfg.sshUser
	if opts.Root {
		user = "root"
	}

	cc := ssh.NewConnConfig(prefixed(name), user, t.cfg.sshKey, t.cfg.knownHosts)
	cc.Env = envToMap(opts.Env)

	hasCustomIO := opts.Stdin != nil || opts.Stdout != nil || opts.Stderr != nil
	if hasCustomIO {
		args := append(ssh.Args(cc), opts.Cmd...)
		cmd := exec.CommandContext(ctx, "ssh", args...)
		cmd.Stdin = opts.Stdin
		cmd.Stdout = opts.Stdout
		cmd.Stderr = opts.Stderr
		if len(cc.Env) > 0 {
			cmd.Env = ssh.EnvWithOverrides(os.Environ(), cc.Env)
		}

		if err := cmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return exitErr.ExitCode(), nil
			}
			return 1, err
		}
		return 0, nil
	}

	return t.ssh.Exec(ctx, cc, opts.Cmd)
}

// Output executes a command and returns its combined stdout.
func (t *TrueNAS) Output(ctx context.Context, name string, cmd []string) ([]byte, error) {
	if _, err := t.ensureRunning(ctx, name); err != nil {
		return nil, err
	}
	cc := ssh.NewConnConfig(prefixed(name), t.cfg.sshUser, t.cfg.sshKey, t.cfg.knownHosts)
	return t.ssh.OutputQuiet(ctx, cc, cmd)
}

// Console attaches an interactive console session.
func (t *TrueNAS) Console(ctx context.Context, name string, opts sandbox.ConsoleOpts) error {
	if _, err := t.ensureRunning(ctx, name); err != nil {
		return err
	}
	cc := ssh.NewConnConfig(prefixed(name), t.cfg.sshUser, t.cfg.sshKey, t.cfg.knownHosts)
	cc.Env = envToMap(opts.Env)
	remoteCmd := strings.Join(opts.RemoteCmd, " ")
	return ssh.Console(cc, remoteCmd)
}

// Ready waits until the instance is RUNNING with a routable IP and
// reachable via SSH. The full timeout covers both IP appearance (cloned
// containers boot DHCP slowly — ~15-60s) and SSH bring-up.
//
// If key auth fails, it pushes the current machine's SSH public key via
// the TrueNAS file API.
func (t *TrueNAS) Ready(ctx context.Context, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	full := prefixed(name)

	// Poll for an instance that is RUNNING with a routable IP. ensureRunning
	// is single-shot; this is the polling equivalent that handles slow DHCP
	// after a fresh boot or rootfs clone.
	if err := retry.Poll(ctx, time.Second, timeout, func(ctx context.Context) (bool, error) {
		inst, err := t.client.Virt.GetInstance(ctx, full)
		if err != nil {
			return false, fmt.Errorf("refreshing instance: %w", err)
		}
		if inst == nil {
			return false, fmt.Errorf("instance %q not found", name)
		}
		if inst.Status != "RUNNING" {
			return false, nil // keep polling; container may still be starting
		}
		return ipFromAliases(inst.Aliases) != "", nil
	}); err != nil {
		if errors.Is(err, retry.ErrTimeout) {
			return fmt.Errorf("waiting for IP on %s: deadline exceeded", name)
		}
		return err
	}

	host := full
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return fmt.Errorf("no time left after IP poll for SSH wait on %s", name)
	}
	if err := t.ssh.WaitReady(ctx, host, remaining, nil); err != nil {
		return err
	}

	// Test key auth and push the key if it fails.
	cc := ssh.NewConnConfig(host, t.cfg.sshUser, t.cfg.sshKey, t.cfg.knownHosts)
	if err := t.ssh.TestAuth(ctx, cc); err != nil {
		pubKey := readSSHPubKey(t.cfg.sshKey)
		if pubKey == "" {
			return fmt.Errorf("SSH key auth failed and no public key at %s.pub", t.cfg.sshKey)
		}
		if writeErr := t.client.WriteAuthorizedKey(ctx, full, pubKey); writeErr != nil {
			return fmt.Errorf("SSH key auth failed; writing key: %w", writeErr)
		}
	}

	// Wait for systemd to reach a stable boot state. TestAuth confirms sshd
	// accepts the connection, but freshly-booted clones can still have unit
	// activations in flight — first exec has been seen returning exit 0 with
	// empty stdout, recovering on retry. `systemctl is-system-running --wait`
	// blocks inside the container until boot stabilises ("running" or
	// "degraded"), giving us a deterministic readiness signal instead of
	// client-side polling. `|| true` swallows the non-zero exit returned for
	// "degraded" (non-essential units that never start are normal); `id` runs
	// in the same SSH session afterward so we can verify the user's session
	// is live and non-empty stdout returns from at least one real command.
	remaining = time.Until(deadline)
	if remaining <= 0 {
		return fmt.Errorf("no time left for readiness probe on %s", name)
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, remaining)
	defer probeCancel()
	out, err := t.ssh.OutputQuiet(probeCtx, cc, []string{
		"systemctl is-system-running --wait >/dev/null 2>&1 || true; id",
	})
	if err != nil {
		return fmt.Errorf("readiness probe on %s: %w", name, err)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return fmt.Errorf("readiness probe on %s returned empty output", name)
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
