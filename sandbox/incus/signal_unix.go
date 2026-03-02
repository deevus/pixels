//go:build !windows

package incus

import (
	"os"
	"syscall"
)

func sigWINCH() os.Signal {
	return syscall.SIGWINCH
}
