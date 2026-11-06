package sandbox

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestLandlockSpecRefusesReadOnlyInsideGrant(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &Policy{Mode: "workspace", Root: root, ReadOnly: []string{"docs"}}
	if _, err := p.landlockSpec(root); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("landlockSpec with a read-only path inside the root = %v, want a refusal", err)
	}
}

func TestLandlockSpecGrants(t *testing.T) {
	root := t.TempDir()
	extra := t.TempDir()
	outside := outsideTempDir(t)
	p := &Policy{Mode: "workspace", Root: root, Network: true, Writable: []string{extra}, ReadOnly: []string{outside}}
	spec, err := p.landlockSpec(root)
	if err != nil {
		t.Fatal(err)
	}
	resolvedExtra, err := canonicalPath(extra)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Write[0] != root || !slices.Contains(spec.Write, resolvedExtra) || !slices.Contains(spec.Write, "/dev/null") {
		t.Fatalf("spec grants %v, want the root, the writable path and the devices", spec.Write)
	}
	if !spec.Network {
		t.Fatal("spec dropped the network")
	}
	encoded, err := json.Marshal(launchSpec{Write: []string{root}})
	if err != nil || strings.Contains(string(encoded), "network") {
		t.Fatalf("offline spec encodes as %s (%v); network must be omitted", encoded, err)
	}
}

func TestLandlockSpecDropsTempDirHoldingMask(t *testing.T) {
	mask, err := canonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(mask, "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	p := &Policy{Mode: "workspace", Root: root, Masked: []string{mask}}
	spec, err := p.landlockSpec(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range spec.Write {
		if grant != root && encloses(grant, mask) {
			t.Fatalf("grant %s re-opens the mask %s", grant, mask)
		}
	}
}

func TestWrapLinuxDispatch(t *testing.T) {
	root, err := canonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	withProbe(t, Capability{Supported: true, NetworkControl: true, Mechanism: mechanismLandlock, Platform: "linux"})
	cmd := exec.Command("/bin/echo", "hi")
	p := &Policy{Mode: "workspace", Backend: "auto", Root: root}
	wrapped, err := p.wrapLinux(context.Background(), cmd, root)
	if runtime.GOOS != "linux" || landlockABI() < 1 {
		if err == nil || !strings.Contains(err.Error(), "landlock") {
			t.Fatalf("landlock wrap without Landlock = %v, want an error naming landlock", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if wrapped.Path != selfExe || wrapped.Args[0] != launcherArg0 || wrapped.Args[2] != "/bin/echo" || !slices.Equal(wrapped.Args[3:], []string{"/bin/echo", "hi"}) {
		t.Fatalf("wrapped = %s %q", wrapped.Path, wrapped.Args)
	}
	var spec launchSpec
	if err := json.Unmarshal([]byte(wrapped.Args[1]), &spec); err != nil || spec.Network || spec.Write[0] != root {
		t.Fatalf("spec %s: %+v %v", wrapped.Args[1], spec, err)
	}
	if wrapped.Dir != root {
		t.Fatalf("dir %q, want %q", wrapped.Dir, root)
	}
}

func TestWrapLinuxExplicitBubblewrapIgnoresLandlockProbe(t *testing.T) {
	root, err := canonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	withProbe(t, Capability{Supported: true, NetworkControl: true, Mechanism: mechanismLandlock, Platform: "linux"})
	p := &Policy{Mode: "workspace", Backend: "bwrap", Root: root}
	wrapped, err := p.wrapLinux(context.Background(), exec.Command("/bin/true"), root)
	if err == nil && wrapped.Path == selfExe {
		t.Fatal("explicit bwrap backend ran through the Landlock launcher")
	}
}

func TestAppArmorUsernsHint(t *testing.T) {
	saved := apparmorUsernsSysctl
	t.Cleanup(func() { apparmorUsernsSysctl = saved })
	sysctl := filepath.Join(t.TempDir(), "apparmor_restrict_unprivileged_userns")
	apparmorUsernsSysctl = sysctl
	if hint := apparmorUsernsHint(); hint != "" {
		t.Fatalf("hint without the sysctl: %q", hint)
	}
	if err := os.WriteFile(sysctl, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if hint := apparmorUsernsHint(); hint != "" {
		t.Fatalf("hint with the restriction off: %q", hint)
	}
	if err := os.WriteFile(sysctl, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hint := apparmorUsernsHint()
	for _, want := range []string{"kernel.apparmor_restrict_unprivileged_userns=1", "userns,", "/etc/apparmor.d/bwrap", "apparmor_parser -r", "sudo sysctl kernel.apparmor_restrict_unprivileged_userns=0"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint lacks %q: %s", want, hint)
		}
	}
}
