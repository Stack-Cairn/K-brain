//go:build unix

package sandbox

import (
	"io/fs"
	"syscall"
)

func hardLinks(info fs.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}
