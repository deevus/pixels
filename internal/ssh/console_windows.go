//go:build windows

package ssh

import (
	"fmt"
	"os"
	"os/exec"
)

// Console runs an interactive SSH session as a child process.
// If env is non-nil, the entries are forwarded via SSH SetEnv.
func Console(host, user, keyPath string, env map[string]string) error {
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("ssh binary not found: %w", err)
	}
	cmd := exec.Command(sshBin, sshArgs(host, user, keyPath, env)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
