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

	"al.essio.dev/pkg/shellescape"

	"github.com/deevus/pixels/internal/ssh"
)

// zmxVersion is the zmx release to install inside containers.
const zmxVersion = "0.4.1"

// Step defines a named provisioning task to run via zmx.
type Step struct {
	Name   string // zmx session name (e.g., "px-egress")
	Script string // shell command to execute
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
	cmd := fmt.Sprintf("zmx run %s bash -c %s >/dev/null 2>&1", step.Name, shellescape.Quote(step.Script))
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
	args := append([]string{"zmx", "wait"}, names...)
	code, err := ssh.ExecQuiet(ctx, r.Host, r.User, r.KeyPath, args)
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
	out, err := ssh.OutputQuiet(ctx, r.Host, r.User, r.KeyPath, []string{"zmx", "list"})
	if err != nil {
		return "", fmt.Errorf("listing zmx sessions: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// History returns the scrollback output of a completed zmx session.
func (r *Runner) History(ctx context.Context, name string) (string, error) {
	out, err := ssh.OutputQuiet(ctx, r.Host, r.User, r.KeyPath, []string{"zmx", "history", name})
	if err != nil {
		return "", fmt.Errorf("getting history for %s: %w", name, err)
	}
	return string(out), nil
}

// Steps returns the provisioning steps to execute based on configuration.
// Steps are ordered: devtools first (needs open network), then egress
// (locks down network). Each step completes before the next starts.
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
			Name: "px-egress",
			// Single-line so zmx list output (newline-delimited) parses correctly.
			Script: `set -euo pipefail; DEBIAN_FRONTEND=noninteractive apt-get install -y -qq -o Dpkg::Options::="--force-confold" nftables dnsutils; /usr/local/bin/pixels-resolve-egress.sh; cp /etc/sudoers.d/pixel.restricted /etc/sudoers.d/pixel; chmod 0440 /etc/sudoers.d/pixel`,
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
