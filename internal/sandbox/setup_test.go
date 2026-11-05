package sandbox

import (
	"context"
	"errors"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestSetupNames(t *testing.T) {
	const sid = "S-1-5-21-1004336348-1177238915-682003330-1001"
	a := namesFor(sid)
	if b := namesFor(strings.ToLower(sid)); a != b {
		t.Fatalf("names depend on the SID's case: %+v vs %+v", a, b)
	}
	other := namesFor("S-1-5-21-1004336348-1177238915-682003330-1002")
	seen := map[string]bool{}
	for _, name := range []string{a.online, a.offline, a.group, a.offlineGroup, other.online, other.offline, other.group, other.offlineGroup} {
		if !strings.HasPrefix(name, "KBrainSbx-") {
			t.Errorf("%q lacks the setup prefix", name)
		}
		if seen[strings.ToLower(name)] {
			t.Errorf("%q is used twice", name)
		}
		seen[strings.ToLower(name)] = true
	}
	for _, user := range []string{a.online, a.offline, other.online, other.offline} {
		// User logon names are at most 20 characters (group names 256).
		if len(user) > 20 {
			t.Errorf("%q is longer than a local user name may be", user)
		}
	}
	if a.sidPrefix() == other.sidPrefix() {
		t.Error("two users share the synthetic SID prefix")
	}

	old := setupNamespace
	setupNamespace = "t"
	defer func() { setupNamespace = old }()
	test := namesFor(sid)
	if !strings.HasPrefix(test.online, "KBrainTst-") || test.key == a.key {
		t.Errorf("test namespace does not separate the accounts: %+v", test)
	}
}

func TestSetupSyntheticSIDs(t *testing.T) {
	n := namesFor("S-1-5-21-1-2-3-1001")
	sidPattern := regexp.MustCompile(`^S-1-9(-\d+)+$`)
	reader := n.readerSID()
	ws := n.workspaceSID(`C:\Users\me\src\app`)
	for _, sid := range []string{reader, ws} {
		if !sidPattern.MatchString(sid) || !strings.HasPrefix(sid, n.sidPrefix()) {
			t.Errorf("synthetic SID %q is malformed", sid)
		}
		// A SID has at most 15 sub-authorities.
		if subs := strings.Count(sid, "-") - 2; subs > 15 {
			t.Errorf("%q has %d sub-authorities", sid, subs)
		}
	}
	if reader == ws {
		t.Error("reader and workspace SIDs collide")
	}
	if n.workspaceSID(`C:\Users\me\src\app\`) != ws && runtime.GOOS == "windows" {
		t.Error("a trailing separator changes the workspace SID")
	}
	if n.workspaceSID(`C:\Users\me\src\other`) == ws {
		t.Error("two workspaces share a SID")
	}
	if n.workspaceSID(`C:\USERS\ME\SRC\APP`) != ws {
		t.Error("the workspace SID depends on the path's case")
	}
}

func TestSetupGUIDs(t *testing.T) {
	n := namesFor("S-1-5-21-1-2-3-1001")
	a, b := n.guid("provider"), n.guid("sublayer")
	if a == b {
		t.Fatal("two filtering objects share a GUID")
	}
	if a != n.guid("provider") {
		t.Fatal("GUIDs are not stable")
	}
	for _, g := range [][16]byte{a, b} {
		if g[6]>>4 != 5 || g[8]>>6 != 2 {
			t.Errorf("GUID %x lacks the version 5 / RFC 4122 bits", g)
		}
	}
	if namesFor("S-1-5-21-1-2-3-1002").guid("provider") == a {
		t.Error("two users share a filtering GUID")
	}
}

func TestSetupHints(t *testing.T) {
	for _, text := range []string{setupHint, repairHint("probe failed"), offlineUnavailable} {
		if !strings.Contains(text, "`kn sandbox setup`") {
			t.Errorf("%q does not name the setup command", text)
		}
	}
	if !strings.Contains(repairHint("probe failed"), "probe failed") {
		t.Error("the repair hint drops the problem")
	}
}

func TestSetupStateDirHonorsDataDirEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", dir)
	if got := setupStateDir(); !strings.HasPrefix(got, dir) || !strings.HasSuffix(got, "sandbox") {
		t.Fatalf("setupStateDir = %q, want the sandbox directory under %q", got, dir)
	}
}

func TestSetupUnsupportedOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("setup is available on Windows")
	}
	if _, err := Setup(context.Background()); !errors.Is(err, ErrSetupUnsupported) {
		t.Fatalf("Setup error = %v, want ErrSetupUnsupported", err)
	}
	if _, err := Uninstall(context.Background()); !errors.Is(err, ErrSetupUnsupported) {
		t.Fatalf("Uninstall error = %v, want ErrSetupUnsupported", err)
	}
	s := SetupStatusNow()
	if s.Available || s.Installed || s.Active || s.Platform != runtime.GOOS {
		t.Fatalf("status off Windows = %+v", s)
	}
	if c := Probe(); c.SetupAvailable || c.SetupHint != "" || c.Mechanism == MechanismSandboxUser {
		t.Fatalf("capability off Windows advertises the setup: %+v", c)
	}
}
