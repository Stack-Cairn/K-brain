//go:build windows

package stackdump

import "errors"

// SignalSupported reports whether OnSignal can dump on a signal here. Windows
// has no SIGUSR1: use the backend's GET /v1/debug/stacks instead.
const SignalSupported = false

// OnSignal does nothing on Windows.
func OnSignal(func(path string, err error)) (stop func()) { return func() {} }

// Signal is not available on Windows.
func Signal(int) error {
	return errors.New("signalling a process for a stack dump is not supported on Windows; use kn debug stacks --backend")
}
