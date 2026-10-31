//go:build !windows

package permissions

import "os"

func readShared(path string) ([]byte, error) {
	return os.ReadFile(path)
}
