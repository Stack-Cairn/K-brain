//go:build windows

package sandbox

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// The unit tests below need no administrator rights. The administrator cycle
// (TestWindowsSandboxUserAdminCycle) creates real accounts and filters and
// runs only where KBRAIN_WINDOWS_ADMIN_TESTS=1 (the Windows CI job).

func TestWindowsWFPStructLayout(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("layout checked on 64-bit Windows")
	}
	for name, got := range map[string]uintptr{
		"FWPM_FILTER0":           unsafe.Sizeof(fwpmFilter0{}),
		"FWPM_FILTER_CONDITION0": unsafe.Sizeof(fwpmFilterCondition0{}),
		"FWPM_SUBLAYER0":         unsafe.Sizeof(fwpmSublayer0{}),
		"FWPM_PROVIDER0":         unsafe.Sizeof(fwpmProvider0{}),
	} {
		want := map[string]uintptr{"FWPM_FILTER0": 200, "FWPM_FILTER_CONDITION0": 40, "FWPM_SUBLAYER0": 72, "FWPM_PROVIDER0": 64}[name]
		if got != want {
			t.Errorf("%s is %d bytes, want %d", name, got, want)
		}
	}
}

func TestWindowsGitSafeDirectoryEnv(t *testing.T) {
	env := []string{"PATH=x", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.editor", "GIT_CONFIG_VALUE_0=vi"}
	got := gitSafeDirectoryEnv(env, []string{`C:\src\app`})
	want := []string{"PATH=x", "GIT_CONFIG_KEY_0=core.editor", "GIT_CONFIG_VALUE_0=vi",
		"GIT_CONFIG_KEY_1=safe.directory", "GIT_CONFIG_VALUE_1=C:/src/app",
		"GIT_CONFIG_KEY_2=safe.directory", "GIT_CONFIG_VALUE_2=C:/src/app/*",
		"GIT_CONFIG_COUNT=3"}
	if !slices.Equal(got, want) {
		t.Fatalf("gitSafeDirectoryEnv = %q\nwant %q", got, want)
	}
	// The credential filter dropped GIT_CONFIG_KEY_0: the broken entries go.
	broken := []string{"PATH=x", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_VALUE_0=vi"}
	got = gitSafeDirectoryEnv(broken, []string{`C:\src\app`})
	want = []string{"PATH=x", "GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0=C:/src/app",
		"GIT_CONFIG_KEY_1=safe.directory", "GIT_CONFIG_VALUE_1=C:/src/app/*", "GIT_CONFIG_COUNT=2"}
	if !slices.Equal(got, want) {
		t.Fatalf("gitSafeDirectoryEnv with gaps = %q\nwant %q", got, want)
	}
	if got := gitSafeDirectoryEnv(env, nil); !slices.Equal(got, env) {
		t.Fatalf("no roots changed the environment: %q", got)
	}
}

func TestWindowsDACLInsertOrder(t *testing.T) {
	n := namesFor("S-1-5-21-1-2-3-1001")
	mk := func(typ, flags byte, sid string) rawACE {
		a, err := newACE(typ, flags, fileReadExecute, sid)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	userDeny := mk(accessDeniedACEType, 0, "S-1-5-32-546")
	userAllow := mk(accessAllowedACEType, 0, "S-1-5-32-545")
	inherited := mk(accessAllowedACEType, aceInherited, "S-1-5-18")
	d := fileDACL{aces: []rawACE{userDeny, userAllow, inherited}}
	deny := mk(accessDeniedACEType, 0, n.readerSID())
	allow := mk(accessAllowedACEType, 0, n.readerSID())
	inh := mk(accessAllowedACEType, aceInherited, n.readerSID())
	d.insert(allow, deny, inh, allow)
	want := []rawACE{deny, userDeny, userAllow, allow, inh, inherited}
	if len(d.aces) != len(want) {
		t.Fatalf("DACL has %d entries, want %d", len(d.aces), len(want))
	}
	for i := range want {
		if string(d.aces[i]) != string(want[i]) {
			t.Fatalf("entry %d is %s type %d, want %s type %d", i, d.aces[i].sid(), d.aces[i].typ(), want[i].sid(), want[i].typ())
		}
	}
	ours := n.sandboxACE(nil)
	if !d.remove(ours) || len(d.aces) != 3 || d.remove(ours) {
		t.Fatalf("remove left %d entries", len(d.aces))
	}
}

// TestWindowsACLTreeRoundTrip grants a synthetic SID on a tree the test owns
// (no privileges needed) and removes it again.
func TestWindowsACLTreeRoundTrip(t *testing.T) {
	root := workspace(t)
	file := filepath.Join(root, "sub", "dir", "f.txt")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	n := namesFor("S-1-5-21-1-2-3-1001")
	ours := n.sandboxACE(nil)
	sid := n.workspaceSID(root)
	target, err := grantTarget(accessAllowedACEType, fileModify, true, true, sid)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyTree(root, target, aclWalkGrant, ours, false); err != nil {
		t.Fatal(err)
	}
	top := daclOf(t, root)
	if !slices.ContainsFunc(top.aces, func(a rawACE) bool {
		return a.sid() == sid && a.flags()&aceInherited == 0 && a.mask() == fileModifyNoDel
	}) {
		t.Fatal("the root lacks its explicit entry without DELETE")
	}
	for _, path := range []string{filepath.Join(root, "sub"), filepath.Join(root, "sub", "dir"), file} {
		if !slices.ContainsFunc(daclOf(t, path).aces, func(a rawACE) bool {
			return a.sid() == sid && a.flags()&aceInheritOnly == 0 && a.mask() == fileModify
		}) {
			t.Fatalf("%s lacks the grant: %s (top %s)", path, sddl(path), sddl(root))
		}
	}
	if left := sandboxACEsUnder(t, root, ours); len(left) == 0 {
		t.Fatal("no entries found after the grant")
	}
	// A protected path below the root is denied throughout, also where the
	// grant gave a non-inheriting descendant explicit entries.
	deny, err := grantTarget(accessDeniedACEType, fileWriteRights, false, true, n.readerSID())
	if err != nil {
		t.Fatal(err)
	}
	if err := applyTree(filepath.Join(root, "sub"), deny, aclWalkDeny, ours, false); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "sub"), filepath.Join(root, "sub", "dir"), file} {
		if !slices.ContainsFunc(daclOf(t, path).aces, func(a rawACE) bool { return a.sid() == n.readerSID() && a.typ() == accessDeniedACEType }) {
			t.Fatalf("%s lacks the deny entry: %s", path, sddl(path))
		}
	}
	if err := removeTree(root, ours); err != nil {
		t.Fatal(err)
	}
	if left := sandboxACEsUnder(t, root, ours); len(left) > 0 {
		t.Fatalf("entries left after removeTree: %q", left)
	}
	if err := os.WriteFile(file, []byte("y"), 0o644); err != nil {
		t.Fatalf("the owner lost access: %v", err)
	}
}

// TestWindowsACLDenyTree denies a synthetic SID on a tree; the deny walk
// reports every failure.
func TestWindowsACLDenyTree(t *testing.T) {
	root := workspace(t)
	file := filepath.Join(root, "sub", "f.txt")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	n := namesFor("S-1-5-21-1-2-3-1001")
	ours := n.sandboxACE(nil)
	target, err := grantTarget(accessDeniedACEType, fileAll, false, true, n.readerSID())
	if err != nil {
		t.Fatal(err)
	}
	if err := applyTree(root, target, aclWalkDeny, ours, true); err != nil {
		t.Fatalf("deny walk: %v", err)
	}
	for _, path := range []string{root, filepath.Join(root, "sub"), file} {
		if !slices.ContainsFunc(daclOf(t, path).aces, func(a rawACE) bool { return a.sid() == n.readerSID() && a.typ() == accessDeniedACEType }) {
			t.Fatalf("%s lacks the deny entry: %s", path, sddl(path))
		}
	}
	if err := removeTree(root, ours); err != nil {
		t.Fatal(err)
	}
	if left := sandboxACEsUnder(t, root, ours); len(left) > 0 {
		t.Fatalf("entries left after removeTree: %q", left)
	}
}

func sddl(path string) string {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err.Error()
	}
	return sd.String()
}

func daclOf(t *testing.T, path string) fileDACL {
	t.Helper()
	h, err := openForDACL(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer windows.CloseHandle(h)
	d, err := readDACL(h)
	if err != nil {
		t.Fatalf("read the DACL of %s: %v", path, err)
	}
	return d
}

// sandboxACEsUnder lists the paths under top (not following links) that carry
// an entry of the sandbox.
func sandboxACEsUnder(t *testing.T, top string, ours func(rawACE) bool) []string {
	t.Helper()
	var found []string
	_ = filepath.WalkDir(top, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		h, err := openForDACL(path)
		if err != nil {
			return nil
		}
		defer windows.CloseHandle(h)
		dacl, err := readDACL(h)
		if err == nil && slices.ContainsFunc(dacl.aces, ours) {
			found = append(found, path)
		}
		if d.Type()&fs.ModeSymlink != 0 || isNameSurrogate(path) {
			return filepath.SkipDir
		}
		return nil
	})
	return found
}

// TestWindowsSandboxUserAdminCycle installs the sandbox accounts, runs
// confined commands online and offline, and uninstalls, checking that nothing
// is left behind. The accounts use the test namespace (KBrainTst-...), and
// the data directory is a temporary one.
func TestWindowsSandboxUserAdminCycle(t *testing.T) {
	if os.Getenv("KBRAIN_WINDOWS_ADMIN_TESTS") != "1" {
		t.Skip("set KBRAIN_WINDOWS_ADMIN_TESTS=1 in an elevated session to run the administrator cycle")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("KBRAIN_WINDOWS_ADMIN_TESTS=1 but the test is not elevated")
	}
	isolateState(t)
	oldNamespace := setupNamespace
	setupNamespace = "t"
	recheck := maskRecheck
	maskRecheck = 0
	ctx := context.Background()
	userSID, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	names := namesFor(userSID)
	t.Cleanup(func() {
		if _, err := Uninstall(ctx); err != nil {
			t.Errorf("cleanup uninstall: %v", err)
		}
		setupNamespace = oldNamespace
		maskRecheck = recheck
		Reprobe()
	})
	// A run interrupted earlier may have left the test accounts behind.
	if _, err := Uninstall(ctx); err != nil {
		t.Fatalf("initial uninstall: %v", err)
	}

	status, err := Setup(ctx)
	if err != nil {
		t.Fatalf("setup: %v (%+v)", err, status)
	}
	t.Logf("status after setup: %+v", status)
	c := Probe()
	t.Logf("capability after setup: %+v", c)
	if !status.Installed || !status.Active || c.Mechanism != MechanismSandboxUser || !c.NetworkControl || !c.ReadMasking || !c.PathProtection || c.SetupHint != "" {
		diagnoseSandboxUser(t)
		t.Fatalf("setup is not active: status %+v, capability %+v", status, c)
	}
	st, err := loadSetupState()
	if err != nil {
		t.Fatal(err)
	}
	if n, err := wfpFiltersPresent(names); err != nil || n != 8 {
		t.Fatalf("filters after setup: %d (%v), want 8", n, err)
	}
	// Setup is idempotent: running it again repairs in place.
	if _, err := Setup(ctx); err != nil {
		t.Fatalf("second setup: %v", err)
	}
	if n, err := wfpFiltersPresent(names); err != nil || n != 8 {
		t.Fatalf("filters after the second setup: %d (%v), want 8", n, err)
	}
	st, err = loadSetupState()
	if err != nil {
		t.Fatal(err)
	}

	rootA, rootB, home := workspace(t), workspace(t), workspace(t)
	online := workspacePolicy(rootA)
	offline := workspacePolicy(rootA)
	offline.Network = false

	t.Run("RunsAsSandboxAccounts", func(t *testing.T) {
		for _, tc := range []struct {
			p   *Policy
			sid string
		}{{online, st.Online.SID}, {offline, st.Offline.SID}} {
			out, ok := helper(t, tc.p, "user")
			if !ok || !strings.Contains(out, "SID="+tc.sid) {
				t.Fatalf("network=%v command did not run as %s: %s", tc.p.Network, tc.sid, out)
			}
		}
	})

	t.Run("OnlineRun", func(t *testing.T) {
		if out, ok := helper(t, online, "write", filepath.Join(rootA, "online.txt")); !ok {
			t.Fatalf("online write in the workspace failed: %s", out)
		}
		if out, ok := helper(t, online, "write", filepath.Join(home, "outside.txt")); ok {
			t.Fatalf("online write outside the workspace succeeded: %s", out)
		}
		if out, ok := helper(t, online, "temp"); !ok {
			t.Fatalf("online temp write failed: %s", out)
		}
		curl := filepath.Join(systemRoot(), "System32", "curl.exe")
		if exists(curl) {
			out, code := sandboxed(t, online, exec.Command(curl, "-sS", "-o", "NUL", "-w", "%{http_code}", "https://github.com"))
			t.Logf("curl.exe https as the online account: exit %d: %s", code, out)
			if got := strings.TrimSpace(out); code != 0 || !(strings.HasPrefix(got, "200") || strings.HasPrefix(got, "30")) {
				t.Errorf("curl.exe https failed as the online account: %d %s", code, out)
			}
		}
		powershell := filepath.Join(systemRoot(), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		out, code := sandboxed(t, online, exec.Command(powershell, "-NoProfile", "-NonInteractive", "-Command",
			"(Invoke-WebRequest -UseBasicParsing -Uri https://github.com).StatusCode"))
		t.Logf("Invoke-WebRequest as the online account: exit %d: %s", code, out)
		if code != 0 || !strings.Contains(out, "200") {
			t.Errorf("Invoke-WebRequest failed as the online account: %d %s", code, out)
		}
	})

	t.Run("OfflineRunHasNoNetwork", func(t *testing.T) {
		var locals []string
		for _, addr := range []string{"127.0.0.1:0", "[::1]:0"} {
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				t.Logf("no listener on %s: %v", addr, err)
				continue
			}
			defer ln.Close()
			go func() {
				for {
					conn, err := ln.Accept()
					if err != nil {
						return
					}
					conn.Close()
				}
			}()
			locals = append(locals, ln.Addr().String())
		}
		if len(locals) == 0 {
			t.Fatal("no loopback listener")
		}
		const external = "1.1.1.1:443"
		for _, local := range locals {
			if out, ok := helper(t, online, "dial", local); !ok {
				t.Fatalf("the online account cannot reach the local listener %s: %s", local, out)
			}
		}
		externalReachable := false
		if conn, err := net.DialTimeout("tcp", external, 5*time.Second); err == nil {
			conn.Close()
			externalReachable = true
		} else {
			t.Logf("%s is unreachable from the runner itself (%v); the external check proves less", external, err)
		}
		if out, ok := helper(t, online, "dial", external); externalReachable && !ok {
			t.Errorf("the online account cannot reach %s: %s", external, out)
		}
		for _, addr := range append([]string{external}, locals...) {
			if out, ok := helper(t, offline, "dial", addr); ok {
				t.Errorf("the offline account reached %s: %s", addr, out)
			} else {
				t.Logf("offline dial %s blocked: %s", addr, out)
			}
		}
		if out, ok := helper(t, offline, "write", filepath.Join(rootA, "offline.txt")); !ok {
			t.Fatalf("offline write in the workspace failed: %s", out)
		}
		if out, ok := helper(t, offline, "temp"); !ok {
			t.Fatalf("offline temp write failed: %s", out)
		}
	})

	t.Run("WorkspaceIsolation", func(t *testing.T) {
		// rootB is used by a sandboxed run, so the sandbox group has write
		// access to it; an offline run of rootA still cannot write it.
		onlineB := workspacePolicy(rootB)
		if out, ok := helper(t, onlineB, "write", filepath.Join(rootB, "b.txt")); !ok {
			t.Fatalf("write in the second workspace failed: %s", out)
		}
		for _, path := range []string{filepath.Join(rootB, "from-a.txt"), filepath.Join(rootB, "b.txt")} {
			if out, ok := helper(t, offline, "write", path); ok {
				t.Fatalf("an offline run of the first workspace wrote %s: %s", path, out)
			}
		}
		if out, ok := helper(t, offline, "remove", filepath.Join(rootB, "b.txt")); ok {
			t.Fatalf("an offline run of the first workspace removed a file of the second: %s", out)
		}
		// Without the setup's accounts nothing else gets write access: a
		// low-integrity command cannot write a workspace the sandbox used.
		other := workspacePolicy(workspace(t))
		other.Backend = windowsMechanism
		if out, ok := helper(t, other, "write", filepath.Join(rootA, "from-low.txt")); ok {
			t.Fatalf("a low-integrity command wrote a sandbox-user workspace: %s", out)
		}
		out, ok := helper(t, online, "write", filepath.Join(rootB, "from-online-a.txt"))
		t.Logf("online run of the first workspace writing the second (documented limitation): ok=%v %s", ok, out)
	})

	t.Run("ProtectedPaths", func(t *testing.T) {
		root := workspace(t)
		hooks := filepath.Join(root, ".git", "hooks")
		if err := os.MkdirAll(hooks, 0o755); err != nil {
			t.Fatal(err)
		}
		config := filepath.Join(root, ".git", "config")
		if err := os.WriteFile(config, []byte("[core]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, network := range []bool{true, false} {
			p := workspacePolicy(root)
			p.Network = network
			if out, ok := helper(t, p, "write", filepath.Join(hooks, "pre-commit")); ok {
				t.Fatalf("network=%v wrote a git hook: %s", network, out)
			}
			if out, ok := helper(t, p, "write", config); ok {
				t.Fatalf("network=%v rewrote .git/config: %s", network, out)
			}
			if out, ok := helper(t, p, "rename", filepath.Join(root, ".git"), filepath.Join(root, "git-moved")); ok {
				t.Fatalf("network=%v renamed .git: %s", network, out)
			}
			if out, ok := helper(t, p, "write", filepath.Join(root, "src.txt")); !ok {
				t.Fatalf("network=%v write next to .git failed: %s", network, out)
			}
		}
	})

	t.Run("MaskedReadsFail", func(t *testing.T) {
		masked := filepath.Join(home, ".ssh")
		if err := os.MkdirAll(masked, 0o700); err != nil {
			t.Fatal(err)
		}
		key := filepath.Join(masked, "id_ed25519")
		if err := os.WriteFile(key, []byte("masked-secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, network := range []bool{true, false} {
			p := workspacePolicy(rootA)
			p.Network = network
			p.Masked = []string{masked}
			if out, ok := helper(t, p, "read", key); ok {
				t.Fatalf("network=%v read a masked file: %s", network, out)
			}
			if out, ok := helper(t, p, "list", masked); ok {
				t.Fatalf("network=%v listed a masked directory: %s", network, out)
			}
			if out, code := sandboxed(t, p, exec.Command(cmdExe(), "/d", "/c", "type", key)); code == 0 || strings.Contains(out, "masked-secret") {
				t.Fatalf("network=%v cmd type of a masked file: exit %d: %s", network, code, out)
			}
		}
		if data, err := os.ReadFile(key); err != nil || string(data) != "masked-secret" {
			t.Fatalf("the masked file is unreadable outside the sandbox: %q %v", data, err)
		}
		// The state file with the accounts' passwords is never readable.
		if out, ok := helper(t, online, "read", st.path); ok {
			t.Fatalf("a sandboxed command read the setup state: %s", out)
		}
	})

	t.Run("FilteredEnvironment", func(t *testing.T) {
		for _, network := range []bool{true, false} {
			p := workspacePolicy(rootA)
			p.Network = network
			cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsSandboxHelper$", "--", "env", "GITHUB_TOKEN", "KBRAIN_TEST_VISIBLE", "TEMP")
			cmd.Env = append(os.Environ(), helperEnv+"=1", "GITHUB_TOKEN=leaked-token", "KBRAIN_TEST_VISIBLE=yes")
			wrapped, err := p.Wrap(ctx, cmd)
			if err != nil {
				t.Fatal(err)
			}
			out, err := wrapped.CombinedOutput()
			if err != nil {
				t.Fatalf("network=%v env helper: %v: %s", network, err, out)
			}
			text := string(out)
			if strings.Contains(text, "leaked-token") || !strings.Contains(text, "KBRAIN_TEST_VISIBLE=yes") || !strings.Contains(text, "k-brain-sandbox") {
				t.Fatalf("network=%v environment of the command:\n%s", network, text)
			}
		}
	})

	t.Run("Shells", func(t *testing.T) {
		for _, network := range []bool{true, false} {
			p := workspacePolicy(rootA)
			p.Network = network
			target := filepath.Join(rootA, "cmd-"+map[bool]string{true: "on", false: "off"}[network]+".txt")
			if out, code := sandboxed(t, p, exec.Command(cmdExe(), "/d", "/c", "echo x>"+target)); code != 0 || !exists(target) {
				t.Fatalf("network=%v cmd.exe: exit %d: %s", network, code, out)
			}
			powershell := filepath.Join(systemRoot(), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
			ps := strings.TrimSuffix(target, ".txt") + "-ps.txt"
			if out, code := sandboxed(t, p, exec.Command(powershell, "-NoProfile", "-NonInteractive", "-Command", "Set-Content -Path '"+ps+"' -Value x")); code != 0 || !exists(ps) {
				t.Errorf("network=%v powershell: exit %d: %s", network, code, out)
			}
			if git, err := exec.LookPath("git"); err == nil {
				repo := filepath.Join(rootA, "repo-"+map[bool]string{true: "on", false: "off"}[network])
				if out, err := exec.Command(git, "init", "-q", repo).CombinedOutput(); err != nil {
					t.Fatalf("git init: %v %s", err, out)
				}
				p.Root = rootA
				gitStatus := exec.Command(git, "-C", repo, "status", "--porcelain")
				if out, code := sandboxed(t, p, gitStatus); code != 0 {
					t.Errorf("network=%v git status: exit %d: %s", network, code, out)
				}
			}
		}
	})

	recorded, err := readACLRoots(filepath.Join(filepath.Dir(st.path), aclRootsFile))
	if err != nil {
		t.Fatalf("read the recorded ACL roots: %v", err)
	}
	t.Logf("%d paths carry the sandbox's access entries", len(recorded))
	if len(recorded) == 0 {
		t.Fatal("no ACL roots were recorded")
	}

	status, err = Uninstall(ctx)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	t.Logf("status after uninstall: %+v", status)
	if status.Installed || status.Active || Probe().Mechanism != windowsMechanism {
		t.Fatalf("still active after uninstall: %+v", status)
	}
	if _, err := Uninstall(ctx); err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
	for _, name := range []string{names.online, names.offline, names.group, names.offlineGroup} {
		if _, _, _, err := windows.LookupSID("", name); err == nil {
			t.Errorf("account %s still exists", name)
		}
	}
	if n, err := wfpFiltersPresent(names); err != nil || n != 0 {
		t.Errorf("filters after uninstall: %d (%v)", n, err)
	}
	if key, err := registry.OpenKey(registry.LOCAL_MACHINE, specialAccountsKey, registry.QUERY_VALUE); err == nil {
		for _, name := range []string{names.online, names.offline} {
			if _, _, err := key.GetIntegerValue(name); !errors.Is(err, registry.ErrNotExist) {
				t.Errorf("logon-screen entry for %s still exists (%v)", name, err)
			}
		}
		key.Close()
	}
	for _, file := range []string{st.path, filepath.Join(filepath.Dir(st.path), aclRootsFile)} {
		if _, err := os.Stat(file); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s still exists (%v)", file, err)
		}
	}
	ours := names.sandboxACE([]string{st.Group.SID, st.OfflineGroup.SID})
	for _, path := range append(recorded, rootA, rootB, home) {
		if left := sandboxACEsUnder(t, path, ours); len(left) > 0 {
			t.Errorf("access entries left under %s: %q", path, left)
		}
	}
	// Without the setup, offline runs fail closed again.
	if _, err := offline.Wrap(ctx, exec.Command(cmdExe(), "/c", "exit", "0")); err == nil || !strings.Contains(err.Error(), "kn sandbox setup") {
		t.Fatalf("offline run after uninstall: %v", err)
	}
}

// diagnoseSandboxUser logs, for a command started as each account, the
// processes the accounts run after a few seconds and the launch traces.
func diagnoseSandboxUser(t *testing.T) {
	t.Helper()
	forgetSandboxState()
	st, err := cachedSandboxState()
	if err != nil {
		t.Logf("diagnosis: no state: %v", err)
		return
	}
	root := workspace(t)
	for _, network := range []bool{false, true} {
		p := workspacePolicy(root)
		p.Network = network
		cmd := exec.Command(cmdExe(), "/d", "/c", "whoami /all & cd & echo x>diag.txt & icacls .")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), traceEnv+"=1")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		wrapped, err := p.wrapSandboxUserWith(ctx, cmd, root, st, false)
		if err != nil {
			cancel()
			t.Logf("diagnosis: wrap: %v", err)
			continue
		}
		var out strings.Builder
		wrapped.Stdout, wrapped.Stderr = &out, &out
		if err := wrapped.Start(); err != nil {
			cancel()
			t.Logf("diagnosis: start: %v", err)
			continue
		}
		done := make(chan error, 1)
		go func() { done <- wrapped.Wait() }()
		select {
		case err := <-done:
			t.Logf("diagnosis: network=%v finished: %v", network, err)
		case <-time.After(8 * time.Second):
			for _, user := range []string{st.Online.Name, st.Offline.Name} {
				list, _ := exec.Command("tasklist", "/v", "/fo", "csv", "/fi", "USERNAME eq "+user).CombinedOutput()
				t.Logf("diagnosis: network=%v processes of %s after 8s:\n%s", network, user, list)
			}
			list, _ := exec.Command("tasklist", "/v", "/fo", "csv", "/fi", "IMAGENAME eq conhost.exe").CombinedOutput()
			t.Logf("diagnosis: network=%v console hosts after 8s:\n%s", network, list)
			err := <-done
			t.Logf("diagnosis: network=%v ended: %v", network, err)
		}
		cancel()
		t.Logf("diagnosis: network=%v output: %s", network, out.String())
		temp := fencedTempPath(root)
		for _, stage := range []string{"1", "2"} {
			data, err := os.ReadFile(filepath.Join(temp, "trace-"+stage+".txt"))
			t.Logf("diagnosis: network=%v trace of stage %s (%v):\n%s", network, stage, err, data)
		}
		_ = os.Remove(filepath.Join(temp, "trace-1.txt"))
		_ = os.Remove(filepath.Join(temp, "trace-2.txt"))
	}
	sessions, _ := exec.Command("query", "session").CombinedOutput()
	t.Logf("diagnosis: sessions:\n%s", sessions)
}
