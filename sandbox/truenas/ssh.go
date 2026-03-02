package truenas

import (
	"context"
	"io"
	"time"

	"github.com/deevus/pixels/internal/ssh"
)

// sshRunner abstracts SSH operations for testability. The default
// implementation (realSSH) delegates to the ssh package functions.
type sshRunner interface {
	ExecQuiet(ctx context.Context, cc ssh.ConnConfig, cmd []string) (int, error)
	OutputQuiet(ctx context.Context, cc ssh.ConnConfig, cmd []string) ([]byte, error)
	WaitReady(ctx context.Context, host string, timeout time.Duration, log io.Writer) error
}

// realSSH is the production sshRunner that delegates to the ssh package.
type realSSH struct{}

func (realSSH) ExecQuiet(ctx context.Context, cc ssh.ConnConfig, cmd []string) (int, error) {
	return ssh.ExecQuiet(ctx, cc, cmd)
}

func (realSSH) OutputQuiet(ctx context.Context, cc ssh.ConnConfig, cmd []string) ([]byte, error) {
	return ssh.OutputQuiet(ctx, cc, cmd)
}

func (realSSH) WaitReady(ctx context.Context, host string, timeout time.Duration, log io.Writer) error {
	return ssh.WaitReady(ctx, host, timeout, log)
}
