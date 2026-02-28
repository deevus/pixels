//go:build !windows

package ssh

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// Console replaces the current process with an interactive SSH session.
func Console(host, user, keyPath string) error {
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("ssh binary not found: %w", err)
	}
	args := append([]string{"ssh"}, sshArgs(host, user, keyPath)...)
	return syscall.Exec(sshBin, args, os.Environ())
}
