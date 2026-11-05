//go:build !unix

package sandbox

import "io/fs"

// hardLinks is only needed by the Unix backends.
func hardLinks(fs.FileInfo) uint64 { return 1 }
