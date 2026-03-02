package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/deevus/pixels/sandbox"
)

const containerPrefix = "px-"

func containerName(name string) string {
	return containerPrefix + name
}

func displayName(name string) string {
	return strings.TrimPrefix(name, containerPrefix)
}

// resolveRunningIP returns the IP of a running container via the sandbox API.
func resolveRunningIP(ctx context.Context, name string) (string, error) {
	sb, err := openSandbox()
	if err != nil {
		return "", err
	}
	defer sb.Close()

	inst, err := sb.Get(ctx, name)
	if err != nil {
		return "", fmt.Errorf("looking up %s: %w", name, err)
	}
	if !inst.Status.IsRunning() {
		return "", fmt.Errorf("pixel %q is %s — start it first", name, inst.Status)
	}
	if len(inst.Addresses) == 0 {
		return "", fmt.Errorf("no IP address for %s", name)
	}
	return inst.Addresses[0], nil
}

func newTabWriter(cmd *cobra.Command) *tabwriter.Writer {
	return tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
}

// readSSHPubKey reads the SSH public key from the path configured in ssh.key.
// It derives the .pub path from the private key path.
func readSSHPubKey() (string, error) {
	keyPath := cfg.SSH.Key
	if keyPath == "" {
		return "", nil
	}
	pubPath := keyPath + ".pub"
	data, err := os.ReadFile(pubPath)
	if err != nil {
		return "", fmt.Errorf("reading SSH public key %s: %w", pubPath, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// sandboxExecutor adapts sandbox.Exec to provision.Executor so that
// provision.Runner can operate through any sandbox backend. All commands
// run as root since provisioning checks access /root/ and zmx sessions.
type sandboxExecutor struct {
	sb   sandbox.Exec
	name string
}

func (e *sandboxExecutor) Exec(ctx context.Context, command []string) (int, error) {
	return e.sb.Run(ctx, e.name, sandbox.ExecOpts{Cmd: command, Root: true})
}

func (e *sandboxExecutor) Output(ctx context.Context, command []string) ([]byte, error) {
	var buf bytes.Buffer
	_, err := e.sb.Run(ctx, e.name, sandbox.ExecOpts{
		Cmd:    command,
		Stdout: &buf,
		Root:   true,
	})
	return buf.Bytes(), err
}
