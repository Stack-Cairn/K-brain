package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/Stack-Cairn/K-brain/internal/update"
)

func updateCLI() error {
	cmd := update.InstallerCommand()
	fmt.Printf("k-brain %s — updating to the latest release via the %s installer\n\n", version, runtime.GOOS)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("update failed: %w", err)
	}
	update.Acknowledge()
	fmt.Println("\nk-brain updated — restart any running sessions to use the new version.")
	return nil
}
