//go:build !windows

package sandbox

import (
	"context"
	"fmt"
	"os/exec"
)

// The native Windows sandbox lives in the *_windows.go files; these stubs keep
// the shared platform switch compiling elsewhere.

func probeWindows() Capability {
	return Capability{Mechanism: "none", Platform: "windows", Reason: "the native Windows sandbox is only built for Windows"}
}

func (p *Policy) wrapWindows(context.Context, *exec.Cmd, string) (*exec.Cmd, error) {
	return nil, fmt.Errorf("the native Windows sandbox is only built for Windows")
}

func checkPlatformRoot(string) error { return nil }
