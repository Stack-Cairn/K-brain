package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestProcessTreeHelper(t *testing.T) {
	if os.Getenv("K_BRAIN_PROCESS_TREE_HELPER") != "1" {
		return
	}
	args := os.Args[len(os.Args)-2:]
	depth, err := strconv.Atoi(args[0])
	if err != nil {
		os.Exit(2)
	}
	if depth > 0 {
		child := exec.Command(os.Args[0], "-test.run=^TestProcessTreeHelper$", "--", strconv.Itoa(depth-1), args[1])
		child.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		_ = child.Process.Release()
	}
	if err := os.WriteFile(filepath.Join(args[1], fmt.Sprintf("node-%d", depth)), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		os.Exit(4)
	}
	time.Sleep(time.Minute)
	os.Exit(0)
}

func startTree(t *testing.T, ctx context.Context, depth int) (*exec.Cmd, []windows.Handle) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestProcessTreeHelper$", "--", strconv.Itoa(depth), dir)
	cmd.Env = append(os.Environ(), "K_BRAIN_PROCESS_TREE_HELPER=1")
	Configure(cmd, false)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = Kill(cmd)
		if cmd.ProcessState == nil {
			_ = cmd.Wait()
		}
	})
	var handles []windows.Handle
	for i := 0; i <= depth; i++ {
		deadline := time.Now().Add(10 * time.Second)
		for {
			data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("node-%d", i)))
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if err == nil && parseErr == nil {
				h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(pid))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = windows.TerminateProcess(h, 1)
					_, _ = windows.WaitForSingleObject(h, 2000)
					_ = windows.CloseHandle(h)
				})
				handles = append(handles, h)
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("tree node %d did not start: read=%v parse=%v", i, err, parseErr)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	return cmd, handles
}

func TestCancelTerminatesWindowsProcessTree(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, handles := startTree(t, ctx, 2)
	unrelated, _ := startTree(t, context.Background(), 0)
	if !Alive(cmd) || !Alive(unrelated) {
		t.Fatal("helper processes are not alive")
	}
	t.Setenv("PATH", t.TempDir())
	start := time.Now()
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("canceled process exited successfully")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("native process tree cancellation took %s", elapsed)
	}
	for i, h := range handles {
		state, err := windows.WaitForSingleObject(h, 0)
		if err != nil || state != windows.WAIT_OBJECT_0 {
			t.Errorf("tree node %d survived cancellation: state=%d err=%v", i, state, err)
		}
	}
	if !Alive(unrelated) {
		t.Fatal("cancellation terminated an unrelated process")
	}
	if Alive(cmd) {
		t.Fatal("waited process reported alive")
	}
	if err := Kill(cmd); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Kill after Wait = %v", err)
	}
}

func TestWindowsProcessNotStarted(t *testing.T) {
	for _, cmd := range []*exec.Cmd{nil, {}} {
		if Alive(cmd) {
			t.Fatal("unstarted process reported alive")
		}
		if err := Kill(cmd); !errors.Is(err, os.ErrProcessDone) {
			t.Fatalf("Kill unstarted process = %v", err)
		}
	}
}
