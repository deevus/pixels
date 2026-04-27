// Package incus implements the sandbox.Sandbox interface using a native
// Incus daemon connection (local unix socket or remote HTTPS).
package incus

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	incusclient "github.com/lxc/incus/v6/client"

	"github.com/deevus/pixels/sandbox"
)

// Compile-time check that Incus implements sandbox.Sandbox.
var _ sandbox.Sandbox = (*Incus)(nil)

func init() {
	sandbox.Register("incus", func(cfg map[string]string) (sandbox.Sandbox, error) {
		return New(cfg)
	})
}

// Incus implements sandbox.Sandbox using a native Incus daemon connection.
type Incus struct {
	server incusclient.InstanceServer
	cfg    *incusCfg

	// Embedded helper provides WriteFile/ReadFile/ListFiles/DeleteFile via Run.
	sandbox.FilesViaExec
}

// New creates an Incus sandbox backend from a flat config map.
func New(cfg map[string]string) (*Incus, error) {
	c, err := parseCfg(cfg)
	if err != nil {
		return nil, err
	}

	server, err := connect(c)
	if err != nil {
		return nil, err
	}

	if c.project != "" {
		server = server.UseProject(c.project)
	}

	i := &Incus{
		server: server,
		cfg:    c,
	}
	i.FilesViaExec = sandbox.FilesViaExec{Exec: i}
	return i, nil
}

// connect establishes a connection to the Incus daemon.
func connect(cfg *incusCfg) (incusclient.InstanceServer, error) {
	if cfg.remote != "" {
		args := &incusclient.ConnectionArgs{}
		if cfg.clientCert != "" && cfg.clientKey != "" {
			cert, err := os.ReadFile(cfg.clientCert)
			if err != nil {
				return nil, fmt.Errorf("reading client cert: %w", err)
			}
			key, err := os.ReadFile(cfg.clientKey)
			if err != nil {
				return nil, fmt.Errorf("reading client key: %w", err)
			}
			args.TLSClientCert = string(cert)
			args.TLSClientKey = string(key)
		}
		if cfg.serverCert != "" {
			cert, err := os.ReadFile(cfg.serverCert)
			if err != nil {
				return nil, fmt.Errorf("reading server cert: %w", err)
			}
			args.TLSServerCert = string(cert)
		} else {
			args.InsecureSkipVerify = true
		}
		return incusclient.ConnectIncus(cfg.remote, args)
	}

	socket := cfg.socket
	if socket == "" {
		socket = "/var/lib/incus/unix.socket"
	}
	return incusclient.ConnectIncusUnix(socket, nil)
}

// tlsCert loads a TLS certificate from PEM files (used internally).
func tlsCert(certPath, keyPath string) (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	return &cert, nil
}

// loadCA loads a CA certificate pool from a PEM file (used internally).
func loadCA(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("failed to parse CA certificate from %s", path)
	}
	return pool, nil
}

// Capabilities advertises that the Incus backend supports all optional features.
func (i *Incus) Capabilities() sandbox.Capabilities {
	return sandbox.Capabilities{
		Snapshots:     true,
		CloneFrom:     true,
		EgressControl: true,
	}
}

// Close is a no-op for the Incus backend (no persistent connection to close).
func (i *Incus) Close() error {
	return nil
}
