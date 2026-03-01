//go:build !windows

package ssh

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// Console replaces the current process with an interactive SSH session.
// If env is non-nil, the entries are forwarded via SSH SetEnv.
func Console(host, user, keyPath string, env map[string]string) error {
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("ssh binary not found: %w", err)
	}
	args := append([]string{"ssh"}, sshArgs(host, user, keyPath, env)...)
	return syscall.Exec(sshBin, args, os.Environ())
}
