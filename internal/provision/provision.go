// Package provision orchestrates container provisioning steps via zmx.
// After SSH bootstrap (handled by rc.local), the Runner SSHes into the
// container as root, installs zmx, and executes named provisioning steps
// using zmx run. Each step runs in its own pty session, enabling structured
// status tracking (zmx list), output capture (zmx history), and interactive
// debugging on failure (zmx attach).
package provision

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/deevus/pixels/internal/ssh"
)

// zmxVersion is the zmx release to install inside containers.
const zmxVersion = "0.4.2-pre"

// Step defines a named provisioning task to run via zmx.
type Step struct {
	Name     string // zmx session name (e.g., "px-egress")
	Script   string // shell command to execute inside zmx
	Finalize string // optional: runs after ALL steps complete (not tracked by zmx)
}

// Runner executes and monitors zmx provisioning steps over SSH.
type Runner struct {
	Host    string
	User    string // typically "root"
	KeyPath string
	Log     io.Writer
}

func (r *Runner) logf(format string, a ...any) {
	if r.Log != nil {
		fmt.Fprintf(r.Log, format+"\n", a...)
	}
}

// zmxCmd wraps a zmx command to clear XDG_RUNTIME_DIR. SSH sessions
// set it to /run/user/0 via PAM, but the provision script runs without
// it, so zmx defaults to /tmp/zmx-<uid>. Clearing it here ensures the
// Runner finds the same sessions the provision script created.
func zmxCmd(cmd string) string {
	return "unset XDG_RUNTIME_DIR && " + cmd
}

// InstallZmx downloads and installs the zmx binary inside the container.
func (r *Runner) InstallZmx(ctx context.Context) error {
	url := fmt.Sprintf("https://zmx.sh/a/zmx-%s-linux-x86_64.tar.gz", zmxVersion)
	script := fmt.Sprintf("curl -fsSL %s | tar xz -C /usr/local/bin/", url)
	r.logf("Installing zmx %s...", zmxVersion)
	code, err := ssh.ExecQuiet(ctx, r.Host, r.User, r.KeyPath, []string{script})
	if err != nil {
		return fmt.Errorf("installing zmx: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("installing zmx: exit code %d", code)
	}
	return nil
}

// Run starts a provisioning step via zmx run. The command returns immediately;
// the step executes in the background inside its own pty session.
func (r *Runner) Run(ctx context.Context, step Step) error {
	r.logf("Starting %s...", step.Name)
	// Single shell string so SSH's remote shell preserves quoting for zmx.
	// Redirect stdout/stderr so SSH doesn't wait for the background zmx
	// session to finish (it inherits the FDs from zmx run).
	cmd := zmxCmd(fmt.Sprintf("zmx run %s %s >/dev/null 2>&1", step.Name, step.Script))
	code, err := ssh.ExecQuiet(ctx, r.Host, r.User, r.KeyPath, []string{cmd})
	if err != nil {
		return fmt.Errorf("starting %s: %w", step.Name, err)
	}
	if code != 0 {
		return fmt.Errorf("starting %s: exit code %d", step.Name, code)
	}
	return nil
}

// Wait blocks until all named zmx sessions complete.
func (r *Runner) Wait(ctx context.Context, names ...string) error {
	cmd := zmxCmd("zmx wait " + strings.Join(names, " "))
	code, err := ssh.ExecQuiet(ctx, r.Host, r.User, r.KeyPath, []string{cmd})
	if err != nil {
		return fmt.Errorf("waiting for steps: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("one or more provisioning steps failed (exit %d)", code)
	}
	return nil
}

// List runs zmx list and returns the raw output. The caller can display
// this directly or parse it for structured status information.
func (r *Runner) List(ctx context.Context) (string, error) {
	out, err := ssh.OutputQuiet(ctx, r.Host, r.User, r.KeyPath, []string{zmxCmd("zmx list")})
	if err != nil {
		return "", fmt.Errorf("listing zmx sessions: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// History returns the scrollback output of a completed zmx session.
func (r *Runner) History(ctx context.Context, name string) (string, error) {
	out, err := ssh.OutputQuiet(ctx, r.Host, r.User, r.KeyPath, []string{zmxCmd("zmx history " + name)})
	if err != nil {
		return "", fmt.Errorf("getting history for %s: %w", name, err)
	}
	return string(out), nil
}

// IsProvisioned checks if the provision sentinel file exists.
func (r *Runner) IsProvisioned(ctx context.Context) bool {
	code, err := ssh.ExecQuiet(ctx, r.Host, r.User, r.KeyPath, []string{"test -f /root/.pixels-provisioned"})
	return err == nil && code == 0
}

// HasProvisionScript checks if the provision script was written to the container.
func (r *Runner) HasProvisionScript(ctx context.Context) bool {
	code, err := ssh.ExecQuiet(ctx, r.Host, r.User, r.KeyPath, []string{"test -x /usr/local/bin/pixels-provision.sh"})
	return err == nil && code == 0
}

// WaitProvisioned polls until provisioning completes, calling setStatus
// with zmx step progress along the way. Returns immediately if no
// provisioning is expected or already complete.
func (r *Runner) WaitProvisioned(ctx context.Context, setStatus func(string)) {
	if r.IsProvisioned(ctx) || !r.HasProvisionScript(ctx) {
		return
	}

	setStatus("Waiting for provisioning...")
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}

		if r.IsProvisioned(ctx) {
			return
		}

		// Show zmx step status if available.
		raw, err := r.List(ctx)
		if err != nil {
			continue
		}
		sessions := ParseSessions(raw)
		var parts []string
		for _, s := range sessions {
			if !strings.HasPrefix(s.Name, "px-") {
				continue
			}
			if s.EndedAt == "" {
				parts = append(parts, s.Name+" running")
			} else {
				parts = append(parts, s.Name+" done")
			}
		}
		if len(parts) > 0 {
			setStatus(strings.Join(parts, ", "))
		}
	}
}

// Steps returns the provisioning steps to execute based on configuration.
// All steps run concurrently via zmx. Steps with a Finalize script have
// that script executed after ALL steps complete — this allows egress
// lockdown to be deferred until devtools finishes downloading.
func Steps(egress string, devtools bool) []Step {
	var steps []Step

	if devtools {
		steps = append(steps, Step{
			Name:   "px-devtools",
			Script: "/usr/local/bin/pixels-setup-devtools.sh",
		})
	}

	isRestricted := egress == "agent" || egress == "allowlist"
	if isRestricted {
		steps = append(steps, Step{
			Name:     "px-egress",
			Script:   "/usr/local/bin/pixels-setup-egress.sh",
			Finalize: "/usr/local/bin/pixels-enable-egress.sh",
		})
	}

	return steps
}

// StepNames returns the names of the given steps.
func StepNames(steps []Step) []string {
	names := make([]string, len(steps))
	for i, s := range steps {
		names[i] = s.Name
	}
	return names
}

// Session holds parsed fields from a zmx list output line.
type Session struct {
	Name     string
	EndedAt  string // unix timestamp or empty if still running
	ExitCode string // exit code or empty if still running
}

// ParseSessions parses zmx list output into sessions.
func ParseSessions(raw string) []Session {
	if raw == "" {
		return nil
	}
	var sessions []Session
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "session_name=") {
			continue
		}
		fields := make(map[string]string)
		for _, part := range strings.Split(line, "\t") {
			if k, v, ok := strings.Cut(part, "="); ok {
				fields[k] = v
			}
		}
		sessions = append(sessions, Session{
			Name:     fields["session_name"],
			EndedAt:  fields["task_ended_at"],
			ExitCode: fields["task_exit_code"],
		})
	}
	return sessions
}

// PollStatus checks zmx list and returns a human-readable status string
// and whether all expected steps are done. Returns ("", false) if zmx
// isn't ready yet or the list fails.
func (r *Runner) PollStatus(ctx context.Context, names []string) (string, bool) {
	raw, err := r.List(ctx)
	if err != nil {
		return "", false
	}
	sessions := ParseSessions(raw)

	// Build a lookup of session states by name.
	state := make(map[string]*Session)
	for i := range sessions {
		if strings.HasPrefix(sessions[i].Name, "px-") {
			state[sessions[i].Name] = &sessions[i]
		}
	}

	// Build status string and check completion.
	var parts []string
	allDone := true
	for _, name := range names {
		s, ok := state[name]
		if !ok {
			parts = append(parts, name+" pending")
			allDone = false
		} else if s.EndedAt == "" {
			parts = append(parts, name+" running")
			allDone = false
		} else if s.ExitCode != "0" {
			parts = append(parts, fmt.Sprintf("%s failed (exit %s)", name, s.ExitCode))
		} else {
			parts = append(parts, name+" done")
		}
	}

	return strings.Join(parts, ", "), allDone
}

// Script generates a self-contained provisioning shell script that installs
// zmx and runs the given steps sequentially. The script is designed to be
// written to the container rootfs and invoked from rc.local via nohup.
func Script(steps []Step) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("set -eu\n")
	b.WriteString("# Generated by pixels — do not edit.\n\n")

	// Idempotency guard.
	b.WriteString("SENTINEL=/root/.pixels-provisioned\n")
	b.WriteString("if [ -f \"$SENTINEL\" ]; then\n")
	b.WriteString("  echo \"[$(date -Iseconds)] Already provisioned, skipping\"\n")
	b.WriteString("  exit 0\n")
	b.WriteString("fi\n\n")

	// Wait for SSH bootstrap to complete (defensive).
	b.WriteString("while [ ! -f /root/.ssh-provisioned ]; do sleep 1; done\n\n")

	// Install zmx.
	fmt.Fprintf(&b, "echo \"[$(date -Iseconds)] Installing zmx %s\"\n", zmxVersion)
	fmt.Fprintf(&b, "curl -fsSL https://zmx.sh/a/zmx-%s-linux-x86_64.tar.gz | tar xz -C /usr/local/bin/\n\n", zmxVersion)

	// Parse zmx's socket directory and ensure it exists.
	b.WriteString("ZMX_SOCKET_DIR=$(zmx --version | awk '/socket_dir/{print $2}')\n")
	b.WriteString("mkdir -p \"$ZMX_SOCKET_DIR\"\n")
	b.WriteString("echo \"[$(date -Iseconds)] zmx socket_dir: $ZMX_SOCKET_DIR\"\n\n")

	// Kill zmx session processes so they don't block container shutdown.
	b.WriteString("cleanup() { pkill -9 -f 'zmx run px-' 2>/dev/null || true; }\n")
	b.WriteString("trap 'cleanup; exit 0' TERM INT\n\n")

	// Start all steps concurrently.
	for _, s := range steps {
		fmt.Fprintf(&b, "echo \"[$(date -Iseconds)] Starting %s\"\n", s.Name)
		fmt.Fprintf(&b, "zmx run %s %s >/dev/null 2>&1\n", s.Name, s.Script)
	}

	// Wait for all steps to complete.
	names := StepNames(steps)
	b.WriteString("echo \"[$(date -Iseconds)] Waiting for steps\"\n")
	fmt.Fprintf(&b, "zmx wait %s\n\n", strings.Join(names, " "))

	// Verify all steps exited 0 — zmx wait returns 0 regardless of task exit code.
	// Dump zmx history on failure for debugging.
	for _, s := range steps {
		fmt.Fprintf(&b, "zmx list | grep 'session_name=%s' | grep -q 'task_exit_code=0' || ", s.Name)
		fmt.Fprintf(&b, "{ echo \"[$(date -Iseconds)] %s failed\"; zmx history %s 2>/dev/null || true; cleanup; exit 1; }\n", s.Name, s.Name)
	}
	b.WriteString("\n")

	// Run finalize scripts (e.g., activate egress lockdown after all
	// steps finish so devtools downloads aren't blocked).
	for _, s := range steps {
		if s.Finalize != "" {
			fmt.Fprintf(&b, "echo \"[$(date -Iseconds)] Enabling %s\"\n", s.Name)
			fmt.Fprintf(&b, "%s\n\n", s.Finalize)
		}
	}

	b.WriteString("cleanup\n")
	b.WriteString("echo \"[$(date -Iseconds)] Provisioning complete\"\n")
	b.WriteString("touch \"$SENTINEL\"\n")
	return b.String()
}
