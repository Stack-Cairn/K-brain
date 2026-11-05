package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func exit(code int) *int { return &code }

// denialFixture is a workspace, a host-writable directory outside every grant
// and a masked directory, all canonical.
func denialFixture(t *testing.T) (p *Policy, root, outside, masked string) {
	t.Helper()
	root = canonical(t, t.TempDir())
	outside = canonical(t, outsideTempDir(t))
	masked = canonical(t, outsideTempDir(t))
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	p = &Policy{Mode: "workspace", Root: root, Masked: []string{masked}, Protected: []string{filepath.Join(root, ".git", "config")}, ReadOnly: []string{"docs"}}
	return p, root, outside, masked
}

func canonical(t *testing.T, path string) string {
	t.Helper()
	resolved, err := canonicalPath(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestExplainFailureClassifiesDenials(t *testing.T) {
	p, root, outside, masked := denialFixture(t)
	target := filepath.Join(outside, "out.txt")
	cases := []struct {
		name, command, output string
		want                  *Denial
	}{
		{"dash redirect", "echo x > " + target, "/bin/sh: 1: cannot create " + target + ": Permission denied",
			&Denial{Kind: "write", Path: target, SuggestedRoot: outside}},
		{"bash on seatbelt", "echo x > " + target, "bash: line 1: " + target + ": Operation not permitted",
			&Denial{Kind: "write", Path: target, SuggestedRoot: outside}},
		{"bubblewrap read-only mount", "touch " + target, "touch: cannot touch '" + target + "': Read-only file system",
			&Denial{Kind: "write", Path: target, SuggestedRoot: outside}},
		{"python", "python3 w.py", "Traceback (most recent call last):\n  File \"/usr/lib/x.py\", line 1\nPermissionError: [Errno 1] Operation not permitted: '" + target + "'",
			// The command did not name the path: no proposal.
			&Denial{Kind: "write", Path: target}},
		{"missing directory", "mkdir -p " + filepath.Join(outside, "a", "b"), "mkdir: cannot create directory '" + filepath.Join(outside, "a") + "': Operation not permitted",
			&Denial{Kind: "write", Path: filepath.Join(outside, "a"), SuggestedRoot: outside}},
		{"protected relative", "git config user.name x", "error: could not lock config file .git/config: Operation not permitted",
			&Denial{Kind: "protected", Path: filepath.Join(root, ".git", "config")}},
		{"configured read-only", "echo > docs/a", "sh: docs/a: Read-only file system",
			&Denial{Kind: "protected", Path: filepath.Join(root, "docs", "a")}},
		{"masked", "cat " + filepath.Join(masked, "id"), "cat: " + filepath.Join(masked, "id") + ": Operation not permitted",
			&Denial{Kind: "masked", Path: filepath.Join(masked, "id")}},
		{"inside the workspace", "echo > own", "sh: " + filepath.Join(root, "own") + ": Permission denied", nil},
		{"relative inside the workspace", "echo > own", "sh: ./own: Permission denied", nil},
		{"no marker", "cat " + target, "cat: " + target + ": No such file or directory", nil},
		{"url", "curl https://example.com/x", "curl: (23) Failure writing output: Permission denied https://example.com/x", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := p.ExplainFailure(tc.command, root, tc.output, exit(1))
			if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			if got != nil && !strings.HasPrefix(got.Hint(), "[sandbox] ") {
				t.Fatalf("hint %q", got.Hint())
			}
		})
	}
}

func TestExplainFailureIgnoresSuccessMissingCommandsAndDisabledPolicies(t *testing.T) {
	p, root, outside, _ := denialFixture(t)
	output := "sh: " + filepath.Join(outside, "x") + ": Permission denied"
	for _, code := range []*int{nil, exit(0), exit(127)} {
		if d := p.ExplainFailure("echo > x", root, output, code); d != nil {
			t.Fatalf("exit %v explained as %+v", code, d)
		}
	}
	off := *p
	off.Mode = "off"
	if d := off.ExplainFailure("echo > x", root, output, exit(1)); d != nil {
		t.Fatalf("disabled policy explained %+v", d)
	}
	var none *Policy
	if d := none.ExplainFailure("echo > x", root, output, exit(1)); d != nil {
		t.Fatalf("nil policy explained %+v", d)
	}
}

func TestExplainFailureSkipsPathsTheHostCannotWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("host permissions are not consulted on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can write a read-only directory, so the host check cannot be shown")
	}
	p, root, outside, _ := denialFixture(t)
	locked := filepath.Join(outside, "locked")
	if err := os.Mkdir(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	target := filepath.Join(locked, "x")
	if d := p.ExplainFailure("echo > "+target, root, "sh: "+target+": Permission denied", exit(1)); d != nil {
		t.Fatalf("host-denied write explained as %+v", d)
	}
	// /etc/passwd is not the user's to write either.
	if d := p.ExplainFailure("echo >> /etc/passwd", root, "sh: /etc/passwd: Permission denied", exit(1)); d != nil {
		t.Fatalf("system file explained as %+v", d)
	}
}

func TestExplainFailurePicksThePathNearestTheError(t *testing.T) {
	p, root, outside, _ := denialFixture(t)
	target := filepath.Join(outside, "x")
	// A writable interpreter path before the refused one must not be taken.
	shell := filepath.Join(outside, "bin", "sh")
	d := p.ExplainFailure("echo > "+target, root, shell+": 1: cannot create "+target+": Permission denied", exit(2))
	if d == nil || d.Path != target {
		t.Fatalf("got %+v, want path %s", d, target)
	}
}

func TestExplainFailureNetwork(t *testing.T) {
	p, root, _, _ := denialFixture(t)
	for _, output := range []string{
		"curl: (6) Could not resolve host: example.com",
		"dial tcp: lookup proxy.golang.org: no such host\ndial tcp 1.2.3.4:443: connect: network is unreachable",
		"npm error getaddrinfo EAI_AGAIN registry.npmjs.org",
	} {
		if d := p.ExplainFailure("fetch", root, output, exit(6)); d == nil || d.Kind != "network" || d.Path != "" {
			t.Fatalf("%q: got %+v", output, d)
		}
	}
	online := *p
	online.Network = true
	if d := online.ExplainFailure("fetch", root, "curl: (6) Could not resolve host: example.com", exit(6)); d != nil {
		t.Fatalf("networked run explained as %+v", d)
	}
	// A file denial outranks the network marker on the same run.
	target := filepath.Join(canonical(t, outsideTempDir(t)), "x")
	d := p.ExplainFailure("echo > "+target, root, "Could not resolve host: x\nsh: "+target+": Permission denied", exit(1))
	if d == nil || d.Kind != "write" {
		t.Fatalf("got %+v, want a write denial", d)
	}
}

func TestSuggestRootRefusesBroadDirectories(t *testing.T) {
	home := homeDir()
	if home == "" {
		t.Skip("no home directory")
	}
	_, _, outside, _ := denialFixture(t)
	for _, path := range []string{
		filepath.Join(string(filepath.Separator), "kbrain-no-such-dir", "x"), // the filesystem root
		filepath.Join(home, "kbrain-no-such-file"),                           // the home directory itself
		filepath.Join(filepath.Dir(home), "kbrain-no-such-file"),             // an ancestor of home
	} {
		if got := suggestRoot(path, nil); got != "" {
			t.Errorf("suggestRoot(%s) = %s", path, got)
		}
	}
	if runtime.GOOS != "windows" {
		if got := suggestRoot("/etc/kbrain-no-such-file", nil); got != "" {
			t.Errorf("suggested the top-level %s", got)
		}
	}
	// A directory holding a credential directory is never proposed.
	masked := filepath.Join(outside, "keys")
	if got := suggestRoot(filepath.Join(outside, "new"), []string{masked}); got != "" {
		t.Errorf("suggested %s, which holds the mask %s", got, masked)
	}
	if got := suggestRoot(filepath.Join(outside, "new", "file"), nil); got != outside {
		t.Errorf("suggestRoot = %q, want %q", got, outside)
	}
}

func TestPathTokens(t *testing.T) {
	for _, tc := range []struct {
		line string
		want string
	}{
		{"/bin/sh: 1: cannot create /x/y: Permission denied", "/x/y"},
		{"PermissionError: [Errno 13] Permission denied: '/x/y'", "/x/y"},
		{"open /x/y: operation not permitted", "/x/y"},
		{"Error: EACCES: permission denied, open '~/x'", "~/x"},
		{"cp: cannot create regular file './a/b': Read-only file system", "./a/b"},
	} {
		lower := strings.ToLower(tc.line)
		tokens := pathTokens(tc.line, markerIndex(lower, denialMarkers))
		if len(tokens) == 0 || tokens[0] != tc.want {
			t.Errorf("%q: tokens %q, want %q first", tc.line, tokens, tc.want)
		}
	}
}

func TestDenialHints(t *testing.T) {
	write := (&Denial{Kind: "write", Path: "/w/x", SuggestedRoot: "/w"}).Hint()
	if !strings.Contains(write, "/w/x") || !strings.Contains(write, "add /w as a writable workspace root") || !strings.Contains(write, "sudo") {
		t.Fatalf("write hint: %q", write)
	}
	if hint := (&Denial{Kind: "write", Path: "/w/x"}).Hint(); strings.Contains(hint, "add ") {
		t.Fatalf("hint without a suggestion proposes a root: %q", hint)
	}
	for _, kind := range []string{"protected", "masked", "network"} {
		if hint := (&Denial{Kind: kind, Path: "/p"}).Hint(); !strings.HasPrefix(hint, "[sandbox] ") {
			t.Fatalf("%s hint: %q", kind, hint)
		}
	}
}

// TestLiveSandboxDenialIsExplained runs a refused write under the platform
// sandbox and checks the real error text is recognized.
func TestLiveSandboxDenialIsExplained(t *testing.T) {
	liveSandbox(t)
	root := t.TempDir()
	outside := canonical(t, outsideTempDir(t))
	p := &Policy{Mode: "workspace", Backend: "auto", Root: root, Network: true, Masked: MaskedDirs(), Protected: ProtectedPaths(root)}
	target := filepath.Join(outside, "denied.txt")
	command := "echo x > " + target
	out, err := confinedRun(t, p, command)
	if err == nil {
		t.Fatalf("write outside the workspace succeeded: %q", out)
	}
	code := 1
	if exitErr, ok := err.(interface{ ExitCode() int }); ok {
		code = exitErr.ExitCode()
	}
	d := p.ExplainFailure(command, root, out, &code)
	t.Logf("output %q, exit %d, denial %+v", out, code, d)
	if d == nil || d.Kind != "write" || d.Path != target || d.SuggestedRoot != outside {
		t.Fatalf("denial %+v, want a write to %s with %s suggested", d, target, outside)
	}
	// A write the sandbox allows that fails for another reason is not blamed on it.
	if err := os.Mkdir(filepath.Join(root, "locked"), 0o555); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		command = "echo x > locked/f"
		out, err = confinedRun(t, p, command)
		if err == nil {
			t.Fatalf("write into a read-only directory succeeded: %q", out)
		}
		if d := p.ExplainFailure(command, root, out, &code); d != nil {
			t.Fatalf("workspace permission error explained as %+v (output %q)", d, out)
		}
	}
}
