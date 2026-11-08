package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/stackdump"
)

// backendWatchdogTimeout is how long the backend's locks or health reply may
// make no progress before the watchdog writes a stack dump.
const backendWatchdogTimeout = 20 * time.Second

const debugUsage = `usage: kn debug stacks <pid>
       kn debug stacks --backend [--url http://127.0.0.1:47321] [--token TOKEN]

Writes every goroutine's stack of a running kn or backend to
<k-brain log dir>/stacks-<time>-<pid>.txt without stopping it, and prints the
path. <pid> works on macOS and Linux (SIGUSR1); --backend works everywhere.`

// debugStacksWait bounds how long `kn debug stacks <pid>` waits for the dump.
var debugStacksWait = 10 * time.Second

func debugCLI(args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(stdout, debugUsage)
		return nil
	}
	if args[0] != "stacks" {
		return fmt.Errorf("unknown debug command %q\n%s", args[0], debugUsage)
	}
	fs := flag.NewFlagSet("kn debug stacks", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	toBackend := fs.Bool("backend", false, "ask the backend over HTTP")
	url := fs.String("url", "http://127.0.0.1:47321", "backend base URL")
	token := fs.String("token", os.Getenv("K_BRAIN_BACKEND_TOKEN"), "backend bearer token; defaults to K_BRAIN_BACKEND_TOKEN")
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("%v\n%s", err, debugUsage)
	}
	if *toBackend {
		if fs.NArg() != 0 {
			return errors.New(debugUsage)
		}
		path, err := backendStacks(*url, *token)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, path)
		return nil
	}
	if fs.NArg() != 1 {
		return errors.New(debugUsage)
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil || pid <= 0 {
		return fmt.Errorf("not a process id: %q\n%s", fs.Arg(0), debugUsage)
	}
	path, err := signalStacks(pid)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, path)
	return nil
}

func backendStacks(base, token string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(base, "/")+"/v1/debug/stacks", nil)
	if err != nil {
		return "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("backend did not answer: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Path  string `json:"path"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	if resp.StatusCode != http.StatusOK || out.Path == "" {
		msg := out.Error
		if msg == "" {
			msg = resp.Status
		}
		return "", fmt.Errorf("backend refused the stack dump: %s", msg)
	}
	return out.Path, nil
}

func signalStacks(pid int) (string, error) {
	dir, err := stackdump.Dir()
	if err != nil {
		return "", err
	}
	suffix := "-" + strconv.Itoa(pid) + ".txt"
	seen := map[string]bool{}
	for _, path := range stackdump.List(dir) {
		seen[path] = true
	}
	if err := stackdump.Signal(pid); err != nil {
		return "", err
	}
	deadline := time.Now().Add(debugStacksWait)
	for time.Now().Before(deadline) {
		for _, path := range stackdump.List(dir) {
			if !seen[path] && strings.HasSuffix(path, suffix) {
				return path, nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", fmt.Errorf("process %d wrote no dump to %s within %v (another data directory, or not a kn process?)", pid, dir, debugStacksWait)
}

// reportStacks prints where a signal-triggered dump went, for processes whose
// stderr is a log rather than a full-screen UI.
func reportStacks(who string) func(string, error) {
	return func(path string, err error) {
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: stack dump failed: %v\n", who, err)
			return
		}
		fmt.Fprintf(os.Stderr, "%s: stacks written to %s\n", who, path)
	}
}
