//go:build !linux

package sandbox

// landlockABI reports no Landlock support outside Linux.
func landlockABI() int { return 0 }
