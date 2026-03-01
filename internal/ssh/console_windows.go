//go:build windows

package ssh

import (
	"fmt"
	"os"
	"os/exec"
)

// Console runs an interactive SSH session as a child process.
// If remoteCmd is non-empty, it is executed in a forced PTY on the remote host.
func Console(cc ConnConfig, remoteCmd string) error {
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("ssh binary not found: %w", err)
	}
	cmd := exec.Command(sshBin, consoleArgs(cc, remoteCmd)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
