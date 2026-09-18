package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/Stack-Cairn/K-brain/internal/browser/extrelay"
)

func browserCLI(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: kn browser <install>")
	}
	switch args[0] {
	case "install":
		return browserInstall()
	default:
		return fmt.Errorf("unknown kn browser subcommand %q (want: install)", args[0])
	}
}

func browserInstall() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := extrelay.ExtensionDir(home)
	written, err := extrelay.WriteExtension(dir)
	if err != nil {
		return fmt.Errorf("write extension: %w", err)
	}

	r, err := extrelay.NewRelay()
	if err != nil {
		return fmt.Errorf("start relay to mint token: %w", err)
	}
	defer func() { _ = r.Close() }()
	statePath, err := extrelay.WriteRelayState(home, r.Addr(), r.Token())
	if err != nil {
		return fmt.Errorf("write relay state: %w", err)
	}

	fmt.Println("kn browser extension written:")
	for _, f := range written {
		fmt.Println("  ", f)
	}
	fmt.Printf("  %s (relay address + token, 0600)\n\n", statePath)

	fmt.Println("Load it into Chrome (3 clicks — Chrome doesn't allow programmatic install):")
	fmt.Println("  1. In chrome://extensions, toggle ON \"Developer mode\" (top right).")
	fmt.Println("  2. Click \"Load unpacked\".")
	fmt.Printf("  3. Select this folder:\n       %s\n\n", dir)

	fmt.Println("Then, to let k-brain drive a tab:")
	fmt.Println("  - Set \"browser\": { \"mode\": \"extension\" } in ~/.k-brain/config.json.")
	fmt.Println("  - Open the tab you want, click the k-brain extension icon (a green ● appears).")
	fmt.Println("  - Click the icon again to detach.")
	fmt.Println()
	fmt.Println("Note: while pinned, Chrome shows a \"k-brain is debugging this browser\" bar —")
	fmt.Println("that's chrome.debugger, the mechanism that lets k-brain drive your real session.")

	openInstallTargets(dir)
	return nil
}

func openInstallTargets(dir string) {
	var urlCmd, dirCmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		urlCmd = exec.CommandContext(context.Background(), "open", "-a", "Google Chrome", "chrome://extensions")
		dirCmd = exec.CommandContext(context.Background(), "open", dir)
	case "windows":
		urlCmd = exec.CommandContext(context.Background(), "cmd", "/c", "start", "", "chrome://extensions")
		dirCmd = exec.CommandContext(context.Background(), "explorer", dir)
	default:
		urlCmd = exec.CommandContext(context.Background(), "xdg-open", "chrome://extensions")
		dirCmd = exec.CommandContext(context.Background(), "xdg-open", dir)
	}
	if urlCmd != nil {
		_ = urlCmd.Start()
	}
	if dirCmd != nil {
		_ = dirCmd.Start()
	}
	fmt.Println()
	fmt.Printf("(opened chrome://extensions and %s for you)\n", filepath.Clean(dir))
}
