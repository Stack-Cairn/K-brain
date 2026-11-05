//go:build windows

package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// The tests below run real commands under the native sandbox. Most of them run
// this test binary again inside the sandbox as a helper (TestWindowsSandboxHelper)
// so each check is a plain Go call whose failure is unambiguous.

const helperEnv = "K_BRAIN_SANDBOX_TEST_HELPER"

// TestWindowsSandboxHelper is the confined side of the tests; it does nothing
// unless started by sandboxHelper.
func TestWindowsSandboxHelper(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	args := flag.Args()
	err := runHelper(args)
	if err != nil {
		fmt.Printf("ERR %v\n", err)
		os.Exit(3)
	}
	fmt.Println("OK")
	os.Exit(0)
}

func runHelper(args []string) error {
	if len(args) == 0 {
		return errors.New("no helper action")
	}
	switch args[0] {
	case "write":
		return os.WriteFile(args[1], []byte("sandboxed"), 0o644)
	case "read":
		_, err := os.ReadFile(args[1])
		return err
	case "list":
		_, err := os.ReadDir(args[1])
		return err
	case "rename":
		return os.Rename(args[1], args[2])
	case "remove":
		return os.Remove(args[1])
	case "temp":
		dir := os.Getenv("TEMP")
		fmt.Println("TEMP=" + dir)
		return os.WriteFile(filepath.Join(dir, "sandbox-temp.txt"), []byte("x"), 0o644)
	case "spawn-write":
		// A grandchild started from inside the sandbox writes the file.
		cmd := exec.Command(filepath.Join(systemRoot(), "System32", "cmd.exe"), "/d", "/c", "echo x>"+args[1])
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, out)
		}
		_, err = os.Stat(args[1])
		return err
	case "sleep-child":
		child := exec.Command(filepath.Join(systemRoot(), "System32", "ping.exe"), "-n", "300", "127.0.0.1")
		if err := child.Start(); err != nil {
			return err
		}
		if err := os.WriteFile(args[1], []byte(strconv.Itoa(child.Process.Pid)), 0o644); err != nil {
			return err
		}
		time.Sleep(5 * time.Minute)
		return nil
	case "openprocess":
		pid, err := strconv.Atoi(args[1])
		if err != nil {
			return err
		}
		h, err := windows.OpenProcess(windows.PROCESS_VM_WRITE|windows.PROCESS_VM_OPERATION|windows.PROCESS_CREATE_THREAD, false, uint32(pid))
		if err != nil {
			return err
		}
		windows.CloseHandle(h)
		return nil
	case "setdacl":
		sd, err := windows.GetNamedSecurityInfo(args[1], windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return err
		}
		dacl, _, err := sd.DACL()
		if err != nil {
			return err
		}
		everyone, _ := windows.CreateWellKnownSid(windows.WinWorldSid)
		acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{AccessPermissions: windows.GENERIC_ALL, AccessMode: windows.GRANT_ACCESS,
			Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeValue: windows.TrusteeValueFromSID(everyone)}}}, dacl)
		if err != nil {
			return err
		}
		return windows.SetNamedSecurityInfo(args[1], windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
	case "dial":
		conn, err := net.DialTimeout("tcp", args[1], 5*time.Second)
		if err == nil {
			conn.Close()
		}
		return err
	case "env":
		for _, key := range args[1:] {
			fmt.Printf("%s=%s\n", key, os.Getenv(key))
		}
		return nil
	case "desktop":
		// Prints the command's desktop and opens the named one.
		h, _, _ := procGetThreadDesktop.Call(uintptr(windows.GetCurrentThreadId()))
		own, _ := userObjectName(h)
		fmt.Println("DESKTOP=" + own)
		name, _ := windows.UTF16PtrFromString(args[1])
		desk, _, callErr := procOpenDesktopW.Call(uintptr(unsafe.Pointer(name)), 0, 0, 0x0001|0x0040|0x0100) // read objects, enumerate, switch
		if desk == 0 {
			return callErr
		}
		procCloseDesktop.Call(desk)
		return nil
	case "user":
		u, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			return err
		}
		fmt.Println("SID=" + u.User.Sid.String())
		return nil
	case "regwrite":
		key, _, err := registry.CreateKey(registry.CURRENT_USER, args[1], registry.SET_VALUE)
		if err != nil {
			return err
		}
		defer key.Close()
		return key.SetStringValue("probe", "x")
	}
	return fmt.Errorf("unknown helper action %q", args[0])
}

// isolateState keeps any state the tests write out of the real data
// directory.
func isolateState(t *testing.T) {
	t.Helper()
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	t.Setenv("K_BRAIN_HOME", "")
}

func workspace(t *testing.T) string {
	t.Helper()
	root, err := canonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// workspacePolicy is a run's policy without masks: the tests never label the
// real credential directories. The masking tests add their own.
func workspacePolicy(root string, writable ...string) *Policy {
	return &Policy{Mode: "workspace", Backend: "auto", Root: root, Network: true, Writable: writable, Protected: ProtectedPaths(root)}
}

// sandboxed runs cmd under p and returns its combined output and exit code.
func sandboxed(t *testing.T, p *Policy, cmd *exec.Cmd) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	wrapped, err := p.Wrap(ctx, cmd)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	wrapped.Env = append(os.Environ(), cmd.Env...)
	wrapped.Stdin = cmd.Stdin
	out, err := wrapped.CombinedOutput()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run: %v: %s", err, out)
		}
		code = exitErr.ExitCode()
	}
	if code == launcherFailureCode {
		t.Fatalf("launcher failed: %s", out)
	}
	return string(out), code
}

func helper(t *testing.T, p *Policy, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestWindowsSandboxHelper$", "--"}, args...)...)
	cmd.Env = []string{helperEnv + "=1"}
	out, code := sandboxed(t, p, cmd)
	if code != 0 && code != 3 {
		t.Fatalf("helper %v exited %d: %s", args, code, out)
	}
	return strings.TrimSpace(out), code == 0
}

func cmdExe() string { return filepath.Join(systemRoot(), "System32", "cmd.exe") }

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestWindowsCapability(t *testing.T) {
	isolateState(t) // no sandbox-user setup: the low-integrity fallback
	c := probeWindows()
	if !c.Supported {
		t.Fatalf("native sandbox unsupported: %s", c.Reason)
	}
	if c.Mechanism != windowsMechanism || c.Platform != "windows" || c.NetworkControl || !c.ReadMasking || !c.PathProtection || c.Reason == "" {
		t.Fatalf("capability = %+v", c)
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"supported": true, "mechanism": "low-integrity", "platform": "windows", "network_control": false, "read_masking": true, "path_protection": true, "setup_available": true}
	for key, value := range want {
		if fields[key] != value {
			t.Fatalf("capability JSON %s = %v, want %v (%s)", key, fields[key], value, data)
		}
	}
	if hint, _ := fields["setup_hint"].(string); !strings.Contains(hint, "kn sandbox setup") {
		t.Fatalf("capability JSON setup_hint = %q", hint)
	}
	t.Logf("capability: %s", data)
}

func TestWindowsOfflineFailsClosed(t *testing.T) {
	isolateState(t)
	root := workspace(t)
	withProbe(t, probeWindows())
	if _, err := ForRun(nil, RunRequest{Root: root, Network: false}); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("offline ForRun = %v, want an offline error", err)
	}
	p, err := ForRun(nil, RunRequest{Root: root, Network: true})
	if err != nil {
		t.Fatalf("online ForRun: %v", err)
	}
	p.Network = false
	if _, err := p.Wrap(context.Background(), exec.Command(cmdExe(), "/c", "exit", "0")); err == nil {
		t.Fatal("offline policy wrapped a command")
	}
}

func TestWindowsWritesInsideWorkspaceOnly(t *testing.T) {
	isolateState(t)
	root, sibling := workspace(t), workspace(t)
	// Files that exist before the first run get the workspace label too.
	existing := filepath.Join(root, "sub", "dir", "existing.txt")
	if err := os.MkdirAll(filepath.Dir(existing), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{existing, filepath.Join(sibling, "a.txt"), filepath.Join(sibling, "b.txt")} {
		if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := workspacePolicy(root)
	if out, ok := helper(t, p, "write", filepath.Join(root, "inside.txt")); !ok || !exists(filepath.Join(root, "inside.txt")) {
		t.Fatalf("write inside the workspace failed: %s", out)
	}
	if out, ok := helper(t, p, "write", existing); !ok {
		t.Fatalf("overwrite of a file that predates the first run failed: %s", out)
	}
	if out, ok := helper(t, p, "rename", filepath.Join(root, "sub"), filepath.Join(root, "sub2")); !ok {
		t.Fatalf("rename inside the workspace failed: %s", out)
	}
	// A file the user creates later inherits the label.
	later := filepath.Join(root, "later.txt")
	if err := os.WriteFile(later, []byte("user"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, ok := helper(t, p, "remove", later); !ok || exists(later) {
		t.Fatalf("remove of a file the user created later failed: %s", out)
	}
	if out, ok := helper(t, p, "write", filepath.Join(sibling, "outside.txt")); ok || exists(filepath.Join(sibling, "outside.txt")) {
		t.Fatalf("write to a sibling directory succeeded: %s", out)
	}
	if out, ok := helper(t, p, "remove", filepath.Join(sibling, "a.txt")); ok || !exists(filepath.Join(sibling, "a.txt")) {
		t.Fatalf("delete outside the workspace succeeded: %s", out)
	}
	if out, ok := helper(t, p, "rename", filepath.Join(sibling, "b.txt"), filepath.Join(root, "stolen.txt")); ok || !exists(filepath.Join(sibling, "b.txt")) {
		t.Fatalf("move of an outside file into the workspace succeeded: %s", out)
	}
	home, _ := os.UserHomeDir()
	profileFile := filepath.Join(home, "k-brain-sandbox-escape-"+randomSuffix(t)+".txt")
	t.Cleanup(func() { _ = os.Remove(profileFile) })
	if out, ok := helper(t, p, "write", profileFile); ok || exists(profileFile) {
		t.Fatalf("write to the user profile succeeded: %s", out)
	}
	if err := os.WriteFile(profileFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, ok := helper(t, p, "remove", profileFile); ok || !exists(profileFile) {
		t.Fatalf("delete in the user profile succeeded: %s", out)
	}
	// The workspace's own cmd.exe write and a write through NUL.
	out, code := sandboxed(t, p, exec.Command(cmdExe(), "/d", "/c", "echo hi>cmd.txt& echo x>NUL"))
	if code != 0 || !exists(filepath.Join(root, "cmd.txt")) {
		t.Fatalf("cmd.exe write in the workspace: %d %s", code, out)
	}
}

func TestWindowsExtraWritableRoot(t *testing.T) {
	isolateState(t)
	root, extra := workspace(t), workspace(t)
	if out, ok := helper(t, workspacePolicy(root, extra), "write", filepath.Join(extra, "granted.txt")); !ok {
		t.Fatalf("write to an extra writable root failed: %s", out)
	}
	if out, ok := helper(t, workspacePolicy(root, extra), "write", filepath.Join(filepath.Dir(extra), "beside-extra.txt")); ok {
		t.Fatalf("write beside the extra root succeeded: %s", out)
	}
}

func TestWindowsProtectedPaths(t *testing.T) {
	isolateState(t)
	root := workspace(t)
	for _, dir := range []string{filepath.Join(root, ".git", "hooks"), filepath.Join(root, ".k-brain"), filepath.Join(root, ".liveagent")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	instructions := filepath.Join(root, ".k-brain", "instructions.md")
	hook := filepath.Join(root, ".git", "hooks", "pre-push")
	for _, path := range []string{instructions, hook} {
		if err := os.WriteFile(path, []byte("trusted"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The repository configuration and the hooks directory it names are
	// protected too.
	gitConfig := filepath.Join(root, ".git", "config")
	if err := os.WriteFile(gitConfig, []byte("[core]\n\thooksPath = shared-hooks\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "shared-hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := workspacePolicy(root)
	for _, path := range []string{filepath.Join(root, ".git", "hooks", "pre-commit"), filepath.Join(root, ".k-brain", "new.md"), instructions, filepath.Join(root, ".liveagent", "x"), gitConfig, filepath.Join(root, "shared-hooks", "pre-commit")} {
		if out, ok := helper(t, p, "write", path); ok {
			t.Fatalf("write to protected %s succeeded: %s", path, out)
		}
	}
	if data, _ := os.ReadFile(instructions); string(data) != "trusted" {
		t.Fatalf("protected file changed: %q", data)
	}
	if out, ok := helper(t, p, "read", instructions); !ok {
		t.Fatalf("protected file unreadable: %s", out)
	}
	for _, move := range [][]string{
		{"rename", filepath.Join(root, ".git"), filepath.Join(root, "moved-git")},
		{"rename", filepath.Join(root, ".git", "hooks"), filepath.Join(root, ".git", "old-hooks")},
		{"remove", filepath.Join(root, ".git", "hooks")},
		{"rename", filepath.Join(root, ".k-brain"), filepath.Join(root, "moved")},
		{"remove", filepath.Join(root, ".liveagent")},
		{"remove", instructions},
		{"remove", hook},
		{"rename", hook, filepath.Join(root, "pre-push")},
		{"remove", gitConfig},
		{"rename", filepath.Join(root, "shared-hooks"), filepath.Join(root, "old-shared-hooks")},
	} {
		if out, ok := helper(t, p, move...); ok {
			t.Fatalf("%v succeeded: %s", move, out)
		}
	}
	for _, path := range []string{instructions, hook, filepath.Join(root, ".liveagent")} {
		if !exists(path) {
			t.Fatalf("%s is gone", path)
		}
	}
	if data, _ := os.ReadFile(gitConfig); !strings.Contains(string(data), "shared-hooks") {
		t.Fatalf("protected .git/config changed: %q", data)
	}
	if out, ok := helper(t, p, "write", filepath.Join(root, ".git", "description")); !ok {
		t.Fatalf("write to .git/description (not protected) failed: %s", out)
	}
}

func TestWindowsFencedTemp(t *testing.T) {
	isolateState(t)
	root := workspace(t)
	out, ok := helper(t, workspacePolicy(root), "temp")
	if !ok {
		t.Fatalf("write to the sandbox temp failed: %s", out)
	}
	temp := ""
	for _, line := range strings.Split(out, "\n") {
		if v, found := strings.CutPrefix(strings.TrimSpace(line), "TEMP="); found {
			temp = v
		}
	}
	if !strings.Contains(temp, "k-brain-sandbox") || !exists(filepath.Join(temp, "sandbox-temp.txt")) {
		t.Fatalf("TEMP = %q, want the fenced directory", temp)
	}
	if out, ok := helper(t, workspacePolicy(root), "write", filepath.Join(os.TempDir(), "k-brain-sandbox-escape-"+randomSuffix(t))); ok {
		t.Fatalf("write to the shared temp directory succeeded: %s", out)
	}
}

func TestWindowsChildrenInheritTheFence(t *testing.T) {
	isolateState(t)
	root, sibling := workspace(t), workspace(t)
	p := workspacePolicy(root)
	if out, ok := helper(t, p, "spawn-write", filepath.Join(root, "child.txt")); !ok {
		t.Fatalf("grandchild write inside failed: %s", out)
	}
	if out, ok := helper(t, p, "spawn-write", filepath.Join(sibling, "child.txt")); ok || exists(filepath.Join(sibling, "child.txt")) {
		t.Fatalf("grandchild wrote outside the workspace: %s", out)
	}
}

func TestWindowsCancelKillsProcessTree(t *testing.T) {
	isolateState(t)
	root := workspace(t)
	pidFile := filepath.Join(root, "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsSandboxHelper$", "--", "sleep-child", pidFile)
	wrapped, err := workspacePolicy(root).Wrap(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	wrapped.Env = append(os.Environ(), helperEnv+"=1")
	if err := wrapped.Start(); err != nil {
		t.Fatal(err)
	}
	var pid int
	for deadline := time.Now().Add(time.Minute); pid == 0; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the confined helper did not start its child")
		}
		if data, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatalf("open the grandchild: %v", err)
	}
	defer windows.CloseHandle(h)
	cancel()
	_ = wrapped.Wait()
	if event, err := windows.WaitForSingleObject(h, 15000); err != nil || event != windows.WAIT_OBJECT_0 {
		t.Fatalf("grandchild %d still running after cancel (%d, %v)", pid, event, err)
	}
}

func TestWindowsPlantedBinaryIsNotRun(t *testing.T) {
	isolateState(t)
	root := workspace(t)
	for _, name := range []string{"hostname.bat", "hostname.cmd"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("@echo PLANTED\r\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := workspacePolicy(root)
	viaShell := exec.Command(cmdExe(), "/d", "/c", "hostname")
	viaShell.Dir = root
	out, code := sandboxed(t, p, viaShell)
	if code != 0 || strings.Contains(out, "PLANTED") {
		t.Fatalf("cmd.exe ran the planted binary: %d %s", code, out)
	}
	bare := &exec.Cmd{Path: "hostname", Args: []string{"hostname"}, Dir: root}
	out, code = sandboxed(t, p, bare)
	if code != 0 || strings.Contains(out, "PLANTED") {
		t.Fatalf("bare program resolved to the planted binary: %d %s", code, out)
	}
}

func TestWindowsCannotEscapeThroughProcessesDACLsOrRegistry(t *testing.T) {
	isolateState(t)
	root, sibling := workspace(t), workspace(t)
	p := workspacePolicy(root)
	if out, ok := helper(t, p, "openprocess", strconv.Itoa(os.Getpid())); ok {
		t.Fatalf("confined command opened an unconfined process for writing: %s", out)
	}
	outside := filepath.Join(sibling, "acl.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, ok := helper(t, p, "setdacl", outside); ok {
		t.Fatalf("confined command changed the DACL of a file outside the workspace: %s", out)
	}
	key := `Software\KBrainSandboxTest-` + randomSuffix(t)
	t.Cleanup(func() { _ = registry.DeleteKey(registry.CURRENT_USER, key) })
	if out, ok := helper(t, p, "regwrite", key); ok {
		t.Fatalf("confined command wrote HKCU: %s", out)
	}
	inside := filepath.Join(root, "acl.txt")
	if err := os.WriteFile(inside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, ok := helper(t, p, "setdacl", inside)
	t.Logf("DACL change of a workspace file allowed=%v (%s)", ok, out)
}

// TestWindowsMaskedDirsAreHidden checks that a confined command cannot read
// or list a masked directory, including files that existed before, files
// created later, files moved in from elsewhere and a subtree that carried a
// weaker explicit label, while this (medium-integrity) process and an
// unconfined command still can, and that files elsewhere stay readable.
func TestWindowsMaskedDirsAreHidden(t *testing.T) {
	isolateState(t)
	recheck := maskRecheck
	maskRecheck = 0
	t.Cleanup(func() { maskRecheck = recheck })
	root, home, elsewhere := workspace(t), workspace(t), workspace(t)
	masked := filepath.Join(home, ".ssh")
	nested := filepath.Join(masked, "sub", "dir")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	key, deep := filepath.Join(masked, "id_ed25519"), filepath.Join(nested, "deep.txt")
	weak := filepath.Join(masked, "weak")
	if err := os.Mkdir(weak, 0o700); err != nil {
		t.Fatal(err)
	}
	// A subtree with an explicit medium no-write-up label (no no-read-up).
	if err := setLabel(weak, protectedLabel, true, false); err != nil {
		t.Fatal(err)
	}
	weakFile := filepath.Join(weak, "weak.txt")
	plain := filepath.Join(elsewhere, "plain.txt")
	for _, path := range []string{key, deep, weakFile, plain} {
		if err := os.WriteFile(path, []byte("masked-secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p := workspacePolicy(root)
	p.Masked = []string{masked, filepath.Join(home, ".aws")} // .aws does not exist
	for _, path := range []string{key, deep, weakFile} {
		if out, ok := helper(t, p, "read", path); ok {
			t.Fatalf("confined read of masked %s succeeded: %s", path, out)
		}
	}
	for _, dir := range []string{masked, nested} {
		if out, ok := helper(t, p, "list", dir); ok {
			t.Fatalf("confined listing of masked %s succeeded: %s", dir, out)
		}
	}
	for _, args := range [][]string{{"/d", "/c", "type", key}, {"/d", "/c", "dir", "/b", masked}} {
		if out, code := sandboxed(t, p, exec.Command(cmdExe(), args...)); code == 0 || strings.Contains(out, "masked-secret") || strings.Contains(out, "id_ed25519") {
			t.Fatalf("confined cmd %v: exit %d: %s", args, code, out)
		}
	}
	powershell := filepath.Join(systemRoot(), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if out, code := sandboxed(t, p, exec.Command(powershell, "-NoProfile", "-NonInteractive", "-Command", "Get-Content -Path '"+key+"'")); code == 0 || strings.Contains(out, "masked-secret") {
		t.Fatalf("confined Get-Content of a masked file: exit %d: %s", code, out)
	}
	if out, ok := helper(t, p, "write", filepath.Join(masked, "planted")); ok {
		t.Fatalf("confined write into a masked directory succeeded: %s", out)
	}
	if out, ok := helper(t, p, "read", plain); !ok {
		t.Fatalf("confined read of an unmasked file failed: %s", out)
	}

	// This process and unconfined tools are unaffected.
	if data, err := os.ReadFile(key); err != nil || string(data) != "masked-secret" {
		t.Fatalf("masked file unreadable outside the sandbox: %q %v", data, err)
	}
	if out, err := exec.Command(cmdExe(), "/d", "/c", "type", key).CombinedOutput(); err != nil || !strings.Contains(string(out), "masked-secret") {
		t.Fatalf("unconfined type of the masked file: %v %s", err, out)
	}
	created := filepath.Join(masked, "created-later")
	if err := os.WriteFile(created, []byte("masked-secret"), 0o600); err != nil {
		t.Fatalf("writing the masked directory outside the sandbox: %v", err)
	}
	if entries, err := os.ReadDir(masked); err != nil || len(entries) == 0 {
		t.Fatalf("listing the masked directory outside the sandbox: %v", err)
	}
	moved := filepath.Join(elsewhere, "moved-in")
	if err := os.WriteFile(moved, []byte("masked-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, filepath.Join(masked, "moved-in")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{created, filepath.Join(masked, "moved-in")} {
		if out, ok := helper(t, p, "read", path); ok {
			t.Fatalf("confined read of %s succeeded: %s", path, out)
		}
	}
	if l, _, _, err := fileLabel(weak); err != nil || !l.satisfies(maskedLabel) {
		t.Fatalf("label of the weakly labelled subtree = %+v (%v)", l, err)
	}
	if !probeWindows().ReadMasking {
		t.Fatal("masked reads fail but the capability reports read_masking=false")
	}
}

// TestWindowsWorkspaceInsideMask is a client's default project inside its
// own (masked) data directory: the workspace stays usable and the rest of the
// data directory stays hidden.
func TestWindowsWorkspaceInsideMask(t *testing.T) {
	isolateState(t)
	data := workspace(t)
	root := filepath.Join(data, "default-project")
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(data, "config.json")
	for _, path := range []string{config, filepath.Join(root, "src", "main.go")} {
		if err := os.WriteFile(path, []byte("apikey"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p := workspacePolicy(root)
	p.Masked = []string{data}
	for range 2 {
		if out, ok := helper(t, p, "write", filepath.Join(root, "f.txt")); !ok {
			t.Fatalf("write in a workspace inside a mask failed: %s", out)
		}
		if out, ok := helper(t, p, "read", filepath.Join(root, "src", "main.go")); !ok {
			t.Fatalf("read in a workspace inside a mask failed: %s", out)
		}
		if out, ok := helper(t, p, "read", config); ok {
			t.Fatalf("read of the mask around the workspace succeeded: %s", out)
		}
		if out, ok := helper(t, p, "write", config); ok {
			t.Fatalf("write of the mask around the workspace succeeded: %s", out)
		}
	}
	if out, code := sandboxed(t, p, exec.Command(cmdExe(), "/d", "/c", "cd & echo ok> g.txt & type g.txt")); code != 0 || !strings.Contains(out, "ok") {
		t.Fatalf("cmd in a workspace inside a mask: exit %d: %s", code, out)
	}
}

// TestWindowsDenialHints feeds the explanation real outputs of confined
// commands.
func TestWindowsDenialHints(t *testing.T) {
	isolateState(t)
	root, masked, outside := workspace(t), workspace(t), workspace(t)
	key := filepath.Join(masked, "key")
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := workspacePolicy(root)
	p.Masked = []string{masked}
	for _, tc := range []struct {
		args []string
		kind string
	}{
		{[]string{"read", key}, "masked"},
		{[]string{"write", filepath.Join(outside, "x.txt")}, "write"},
		{[]string{"write", filepath.Join(root, ".git", "hooks", "pre-commit")}, "protected"},
	} {
		out, ok := helper(t, p, tc.args...)
		if ok {
			t.Fatalf("%v succeeded", tc.args)
		}
		code := 3
		d := p.ExplainFailure(strings.Join(tc.args, " "), root, out, &code)
		if d == nil || d.Kind != tc.kind {
			t.Fatalf("%v: output %q explained as %+v, want %s", tc.args, out, d, tc.kind)
		}
	}
}

func TestWindowsLabelsAreIdempotent(t *testing.T) {
	isolateState(t)
	root := workspace(t)
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	masked := filepath.Join(workspace(t), "masked")
	if err := os.Mkdir(masked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(masked, "key"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	recheck := maskRecheck
	maskRecheck = 0
	t.Cleanup(func() { maskRecheck = recheck })
	p := workspacePolicy(root)
	p.Masked = []string{masked}
	sddl := func(path string) string {
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.LABEL_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		return sd.String()
	}
	if _, err := p.Wrap(context.Background(), exec.Command(cmdExe(), "/c", "exit", "0")); err != nil {
		t.Fatal(err)
	}
	paths := []string{root, filepath.Join(root, ".git"), filepath.Join(root, ".git", "hooks"), masked, filepath.Join(masked, "key")}
	var before []string
	for _, path := range paths {
		before = append(before, sddl(path))
	}
	for range 3 {
		if _, err := p.Wrap(context.Background(), exec.Command(cmdExe(), "/c", "exit", "0")); err != nil {
			t.Fatal(err)
		}
	}
	for i, path := range paths {
		if after := sddl(path); after != before[i] {
			t.Fatalf("security of %s changed across runs: %s -> %s", path, before[i], after)
		}
	}
	for path, want := range map[string]integrityLabel{root: writableLabel, filepath.Join(root, ".git"): writableLabel, filepath.Join(root, ".git", "hooks"): protectedLabel, masked: maskedLabel, filepath.Join(masked, "key"): maskedLabel} {
		if l, _, _, err := fileLabel(path); err != nil || l != want {
			t.Fatalf("label of %s = %+v (%v), want %+v", path, l, err, want)
		}
	}
}

// TestWindowsLabelingDoesNotFollowJunctions puts a junction to an outside
// directory in a workspace before its first run.
func TestWindowsLabelingDoesNotFollowJunctions(t *testing.T) {
	isolateState(t)
	root, target := workspace(t), workspace(t)
	victim := filepath.Join(target, "victim.txt")
	if err := os.WriteFile(victim, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if out, err := exec.Command(cmdExe(), "/d", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v %s", err, out)
	}
	p := workspacePolicy(root)
	if out, ok := helper(t, p, "write", filepath.Join(link, "through-link.txt")); ok || exists(filepath.Join(target, "through-link.txt")) {
		t.Fatalf("write through a junction succeeded: %s", out)
	}
	if out, ok := helper(t, p, "write", victim); ok {
		t.Fatalf("write to the junction target succeeded: %s", out)
	}
	for _, path := range []string{target, victim} {
		if l, _, _, err := fileLabel(path); err != nil || l != protectedLabel {
			t.Fatalf("label of %s = %+v (%v): labelling followed the junction", path, l, err)
		}
	}
	// Masking does not follow a junction either.
	masked := filepath.Join(workspace(t), "masked")
	if err := os.Mkdir(masked, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(cmdExe(), "/d", "/c", "mklink", "/J", filepath.Join(masked, "link"), target).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v %s", err, out)
	}
	p.Masked = []string{masked}
	if out, ok := helper(t, p, "read", victim); !ok {
		t.Fatalf("masking followed a junction: %s", out)
	}
	if l, _, _, err := fileLabel(victim); err != nil || l != protectedLabel {
		t.Fatalf("label of %s = %+v (%v): masking followed the junction", victim, l, err)
	}
}

func TestWindowsRefusedRoots(t *testing.T) {
	home, _ := os.UserHomeDir()
	target := t.TempDir()
	junction := filepath.Join(t.TempDir(), "link")
	if out, err := exec.Command(cmdExe(), "/d", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v %s", err, out)
	}
	for _, root := range []string{`C:\`, home, filepath.Dir(home), systemRoot(), filepath.Join(systemRoot(), "System32"), os.Getenv("ProgramFiles"), `\\server\share\project`, junction, filepath.Join(junction, "sub")} {
		if err := checkPlatformRoot(root); err == nil {
			t.Errorf("root %q accepted", root)
		}
	}
	if err := checkPlatformRoot(target); err != nil {
		t.Fatalf("plain root refused: %v", err)
	}
}

func TestWindowsShells(t *testing.T) {
	isolateState(t)
	root := workspace(t)
	p := workspacePolicy(root)
	shells := []struct {
		name string
		path string
		args []string
	}{
		{"cmd", cmdExe(), []string{"/d", "/c", "echo sandboxed-cmd& echo x>cmd-shell.txt"}},
		{"powershell", filepath.Join(systemRoot(), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"), []string{"-NoProfile", "-NonInteractive", "-Command", "Write-Output sandboxed-powershell; Set-Content -Path powershell-shell.txt -Value x"}},
	}
	if pwsh, err := exec.LookPath("pwsh.exe"); err == nil && !strings.Contains(strings.ToLower(pwsh), "windowsapps") {
		shells = append(shells, struct {
			name string
			path string
			args []string
		}{"pwsh", pwsh, []string{"-NoProfile", "-NonInteractive", "-Command", "Write-Output sandboxed-pwsh; Set-Content -Path pwsh-shell.txt -Value x"}})
	}
	if bash := filepath.Join(os.Getenv("ProgramFiles"), "Git", "bin", "bash.exe"); exists(bash) {
		shells = append(shells, struct {
			name string
			path string
			args []string
		}{"bash", bash, []string{"-c", "echo sandboxed-bash; echo x > bash-shell.txt"}})
	}
	for _, shell := range shells {
		t.Run(shell.name, func(t *testing.T) {
			cmd := exec.Command(shell.path, shell.args...)
			cmd.Dir = root
			out, code := sandboxed(t, p, cmd)
			if code != 0 || !strings.Contains(out, "sandboxed-"+shell.name) || !exists(filepath.Join(root, shell.name+"-shell.txt")) {
				t.Fatalf("%s under the sandbox: exit %d: %s", shell.name, code, out)
			}
		})
	}
}

func TestWindowsExitCodeAndStdin(t *testing.T) {
	isolateState(t)
	p := workspacePolicy(workspace(t))
	if _, code := sandboxed(t, p, exec.Command(cmdExe(), "/d", "/c", "exit 7")); code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
	findstr := exec.Command(filepath.Join(systemRoot(), "System32", "findstr.exe"), "hello")
	findstr.Stdin = strings.NewReader("bye\r\nhello sandbox\r\n")
	if out, code := sandboxed(t, p, findstr); code != 0 || !strings.Contains(out, "hello sandbox") || strings.Contains(out, "bye") {
		t.Fatalf("stdin not piped: %d %q", code, out)
	}
}

// TestWindowsHTTPS checks that TLS clients using the system's TLS provider work
// under the sandbox (a restricted token breaks them).
func TestWindowsHTTPS(t *testing.T) {
	if os.Getenv("CI") == "" {
		t.Skip("needs network; runs in CI")
	}
	isolateState(t)
	p := workspacePolicy(workspace(t))
	curl := filepath.Join(systemRoot(), "System32", "curl.exe")
	if exists(curl) {
		out, code := sandboxed(t, p, exec.Command(curl, "-sS", "-o", "NUL", "-w", "%{http_code}", "https://github.com"))
		t.Logf("curl.exe https under the sandbox: exit %d: %s", code, out)
		if code != 0 {
			t.Errorf("curl.exe https failed under the sandbox: %d %s", code, out)
		}
	}
	ps := filepath.Join(systemRoot(), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	out, code := sandboxed(t, p, exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", "(Invoke-WebRequest -UseBasicParsing https://github.com).StatusCode"))
	t.Logf("Invoke-WebRequest under the sandbox: exit %d: %s", code, out)
	if code != 0 || !strings.Contains(out, "200") {
		t.Errorf("Invoke-WebRequest failed under the sandbox: %d %s", code, out)
	}
}

func randomSuffix(t *testing.T) string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}
