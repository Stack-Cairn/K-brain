//go:build !windows

package browser

import (
	"errors"
	"syscall"
)

// processTreeGone reports whether no process of the group led by pid is left.
func processTreeGone(pid int) bool {
	return errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH)
}
