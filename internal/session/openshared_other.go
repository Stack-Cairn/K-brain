//go:build !windows

package session

import "os"

// openShared 在 Windows 以外就是 os.Open：打开的文件不妨碍改名替换。
func openShared(path string) (*os.File, error) {
	return os.Open(path)
}
