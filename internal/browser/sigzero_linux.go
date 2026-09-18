package browser

import "syscall"

func sigzero() syscall.Signal { return syscall.Signal(0) }
