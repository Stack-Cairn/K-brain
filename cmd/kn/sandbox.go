package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/sandbox"
)

const sandboxUsage = "usage: kn sandbox <setup|uninstall|status> [--json]  (status is also --status)"

// sandboxCLI installs, removes or reports the elevated level of the native
// Windows sandbox. Setup and uninstall ask for administrator approval.
func sandboxCLI(args []string) error {
	asJSON := false
	op := ""
	for _, arg := range args {
		switch arg {
		case "--json":
			asJSON = true
		case "--status", "status":
			op = setOp(op, "status")
		case "setup", "install":
			op = setOp(op, "setup")
		case "uninstall", "remove":
			op = setOp(op, "uninstall")
		case "-h", "--help", "help":
			fmt.Println(sandboxUsage)
			return nil
		default:
			return fmt.Errorf("unknown argument %q\n%s", arg, sandboxUsage)
		}
	}
	if op == "" || op == "conflict" {
		return errors.New(sandboxUsage)
	}
	ctx := context.Background()
	var (
		status sandbox.SetupStatus
		err    error
	)
	switch op {
	case "setup":
		if !asJSON {
			fmt.Println("Asking for administrator approval to create the sandbox accounts and network filters...")
		}
		status, err = sandbox.Setup(ctx)
	case "uninstall":
		if !asJSON {
			fmt.Println("Asking for administrator approval to remove the sandbox accounts and network filters...")
		}
		status, err = sandbox.Uninstall(ctx)
	default:
		status = sandbox.SetupStatusNow()
	}
	if asJSON {
		out := map[string]any{"status": status}
		if err != nil {
			out["error"] = err.Error()
		}
		data, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(data))
		return err
	}
	printSandboxStatus(os.Stdout, status)
	return err
}

func setOp(current, next string) string {
	if current != "" && current != next {
		return "conflict"
	}
	return next
}

func printSandboxStatus(w *os.File, s sandbox.SetupStatus) {
	fmt.Fprintf(w, "platform:        %s\n", s.Platform)
	fmt.Fprintf(w, "mechanism:       %s\n", s.Mechanism)
	fmt.Fprintf(w, "network control: %v\n", s.NetworkControl)
	if !s.Available {
		fmt.Fprintln(w, "setup:           not available on this platform")
		return
	}
	fmt.Fprintf(w, "setup installed: %v\n", s.Installed)
	fmt.Fprintf(w, "setup active:    %v\n", s.Active)
	if len(s.Accounts) > 0 {
		fmt.Fprintf(w, "accounts:        %s\n", strings.Join(s.Accounts, ", "))
		fmt.Fprintf(w, "groups:          %s\n", strings.Join(s.Groups, ", "))
	}
	if s.StatePath != "" {
		fmt.Fprintf(w, "state:           %s\n", s.StatePath)
	}
	if s.Problem != "" {
		fmt.Fprintf(w, "problem:         %s\n", s.Problem)
	}
	if s.Hint != "" {
		fmt.Fprintf(w, "hint:            %s\n", s.Hint)
	}
}
