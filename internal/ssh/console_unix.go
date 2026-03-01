//go:build !windows

package ssh

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// Console replaces the current process with an interactive SSH session.
// If remoteCmd is non-empty, it is executed in a forced PTY on the remote host.
func Console(cc ConnConfig, remoteCmd string) error {
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("ssh binary not found: %w", err)
	}
	args := append([]string{"ssh"}, consoleArgs(cc, remoteCmd)...)
	return syscall.Exec(sshBin, args, os.Environ())
}
