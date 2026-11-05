//go:build !unix

package sandbox

// hostWritable has no cheap answer here; the denial is reported as found.
func hostWritable(string) bool { return true }
