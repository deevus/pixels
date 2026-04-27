// Package sandbox defines backend-agnostic interfaces for container lifecycle,
// execution, and network policy. Concrete implementations live in separate
// packages and register themselves via [Register].
package sandbox

import (
	"context"
	"io"
	"os"
	"time"
)

// Backend manages the lifecycle of sandbox instances and their snapshots.
type Backend interface {
	Create(ctx context.Context, opts CreateOpts) (*Instance, error)
	Get(ctx context.Context, name string) (*Instance, error)
	List(ctx context.Context) ([]Instance, error)
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Delete(ctx context.Context, name string) error

	CreateSnapshot(ctx context.Context, name, label string) error
	ListSnapshots(ctx context.Context, name string) ([]Snapshot, error)
	DeleteSnapshot(ctx context.Context, name, label string) error
	RestoreSnapshot(ctx context.Context, name, label string) error
	CloneFrom(ctx context.Context, source, label, newName string) error

	Capabilities() Capabilities
	Close() error
}

// Exec runs commands and interactive sessions inside a sandbox instance.
type Exec interface {
	Run(ctx context.Context, name string, opts ExecOpts) (exitCode int, err error)
	Output(ctx context.Context, name string, cmd []string) ([]byte, error)
	Console(ctx context.Context, name string, opts ConsoleOpts) error
	Ready(ctx context.Context, name string, timeout time.Duration) error
}

// NetworkPolicy controls egress filtering for a sandbox instance.
type NetworkPolicy interface {
	SetEgressMode(ctx context.Context, name string, mode EgressMode) error
	AllowDomain(ctx context.Context, name, domain string) error
	DenyDomain(ctx context.Context, name, domain string) error
	GetPolicy(ctx context.Context, name string) (*Policy, error)
}

// FileEntry describes one entry in a ListFiles result.
type FileEntry struct {
	Path  string      `json:"path"`
	Size  int64       `json:"size"`
	Mode  os.FileMode `json:"mode"`
	IsDir bool        `json:"is_dir"`
}

// Files provides byte-level file I/O into a sandbox instance.
type Files interface {
	WriteFile(ctx context.Context, name, path string, content []byte, mode os.FileMode) error
	ReadFile(ctx context.Context, name, path string, maxBytes int64) (content []byte, truncated bool, err error)
	ListFiles(ctx context.Context, name, path string, recursive bool) ([]FileEntry, error)
	DeleteFile(ctx context.Context, name, path string) error
}

// Sandbox composes all sandbox capabilities into a single interface.
type Sandbox interface {
	Backend
	Exec
	Files
	NetworkPolicy
}

// Status represents a container's lifecycle state.
type Status string

const (
	StatusRunning Status = "RUNNING"
	StatusStopped Status = "STOPPED"
)

// IsRunning reports whether the container is in a running state.
func (s Status) IsRunning() bool { return s == StatusRunning }

// Instance is the backend-agnostic representation of a container.
type Instance struct {
	Name      string
	Status    Status
	Addresses []string
}

// Snapshot is a point-in-time capture of an instance's filesystem.
type Snapshot struct {
	Label string
	Size  int64
}

// CreateOpts holds parameters for creating a new sandbox instance.
type CreateOpts struct {
	Name   string
	Image  string
	CPU    string
	Memory int64
	Bare   bool // create instance only, skip provisioning and SSH wait
}

// ExecOpts holds parameters for running a command inside a sandbox.
type ExecOpts struct {
	Cmd    []string
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Root   bool // run as root instead of the configured sandbox user
}

// ConsoleOpts holds parameters for attaching an interactive console.
type ConsoleOpts struct {
	Env       []string
	RemoteCmd []string
}

// EgressMode controls what network traffic a sandbox may initiate.
type EgressMode string

const (
	EgressUnrestricted EgressMode = "unrestricted"
	EgressAgent        EgressMode = "agent"
	EgressAllowlist    EgressMode = "allowlist"
)

// Policy describes the current egress policy for a sandbox instance.
type Policy struct {
	Mode    EgressMode
	Domains []string
}

// Capabilities advertises optional features a backend supports.
type Capabilities struct {
	Snapshots     bool
	CloneFrom     bool
	EgressControl bool
}
