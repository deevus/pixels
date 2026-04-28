// Package truenas implements the sandbox.Sandbox interface using TrueNAS
// Incus containers via the WebSocket API.
package truenas

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/deevus/pixels/sandbox"
)

// Compile-time check that TrueNAS implements sandbox.Sandbox.
var _ sandbox.Sandbox = (*TrueNAS)(nil)

func init() {
	sandbox.Register("truenas", func(cfg map[string]string) (sandbox.Sandbox, error) {
		return New(cfg)
	})
}

// TrueNAS implements sandbox.Sandbox using the TrueNAS WebSocket API for
// container lifecycle, SSH for execution, and the local cache for fast lookups.
type TrueNAS struct {
	client *Client
	cfg    *tnConfig
	ssh    sshRunner
	warn   io.Writer

	// Embedded helper provides WriteFile/ReadFile/ListFiles/DeleteFile via Run.
	sandbox.FilesViaExec
}

func (t *TrueNAS) warnf(format string, a ...any) {
	if t.warn != nil {
		fmt.Fprintf(t.warn, "pixels: "+format+"\n", a...)
	}
}

// New creates a TrueNAS sandbox backend from a flat config map.
func New(cfg map[string]string) (*TrueNAS, error) {
	c, err := parseCfg(cfg)
	if err != nil {
		return nil, err
	}

	client, err := connect(context.Background(), c)
	if err != nil {
		return nil, err
	}

	t := &TrueNAS{
		client: client,
		cfg:    c,
		ssh:    realSSH{},
		warn:   os.Stderr,
	}
	t.FilesViaExec = sandbox.FilesViaExec{Exec: t}
	return t, nil
}

// NewForTest creates a TrueNAS backend with injected dependencies for testing.
func NewForTest(client *Client, ssh sshRunner, cfg map[string]string) (*TrueNAS, error) {
	c, err := parseCfg(cfg)
	if err != nil {
		return nil, err
	}
	t := &TrueNAS{
		client: client,
		cfg:    c,
		ssh:    ssh,
		warn:   os.Stderr,
	}
	t.FilesViaExec = sandbox.FilesViaExec{Exec: t}
	return t, nil
}

// Capabilities advertises that TrueNAS supports all optional features.
func (t *TrueNAS) Capabilities() sandbox.Capabilities {
	return sandbox.Capabilities{
		Snapshots:     true,
		CloneFrom:     true,
		EgressControl: true,
	}
}

// WriteFile writes content to a file inside the container via the TrueNAS
// filesystem API (no SSH required). Overrides the embedded FilesViaExec.WriteFile
// so file uploads work even before SSH provisioning has set up authorized_keys
// (e.g. during BuildBase).
//
// The TrueNAS filesystem API writes as root. When uid/gid are non-negative,
// the file is chowned to uid:gid via SSH-as-root after the write so callers
// (notably the MCP layer) can produce files owned by the configured exec
// user. uid<0 or gid<0 leaves the file root-owned, matching the historical
// BuildBase behaviour.
func (t *TrueNAS) WriteFile(ctx context.Context, name, path string, content []byte, mode os.FileMode, uid, gid int) error {
	if err := t.client.WriteContainerFile(ctx, prefixed(name), path, content, mode); err != nil {
		return err
	}
	if uid < 0 || gid < 0 {
		return nil
	}
	owner := fmt.Sprintf("%d:%d", uid, gid)
	if _, err := t.Run(ctx, name, sandbox.ExecOpts{
		Cmd:  []string{"chown", "--", owner, path},
		Root: true,
	}); err != nil {
		return fmt.Errorf("chown %s to %s: %w", path, owner, err)
	}
	return nil
}

// Close closes the underlying TrueNAS WebSocket connection.
func (t *TrueNAS) Close() error {
	return t.client.Close()
}
