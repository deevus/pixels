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

// Close closes the underlying TrueNAS WebSocket connection.
func (t *TrueNAS) Close() error {
	return t.client.Close()
}
