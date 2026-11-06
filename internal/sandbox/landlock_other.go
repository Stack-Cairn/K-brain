//go:build !linux

package sandbox

import "errors"

// landlockABI reports no Landlock support outside Linux.
func landlockABI() int { return 0 }

// landlockProtection reports that protected paths need the Linux supervisor.
func landlockProtection() error { return errors.New("Landlock is only available on Linux") }
