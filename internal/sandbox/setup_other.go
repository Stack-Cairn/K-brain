//go:build !windows

package sandbox

import "context"

func setupPlatform(context.Context) error     { return ErrSetupUnsupported }
func uninstallPlatform(context.Context) error { return ErrSetupUnsupported }
func statusPlatform(*SetupStatus)             {}
