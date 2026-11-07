package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Denial explains why a sandboxed command most likely failed. It is a hint for
// the model and the client, recognized from the command's output; nothing is
// granted from it.
type Denial struct {
	// Kind is "write" (a write outside the writable roots), "protected" (a
	// protected or configured read-only path), "masked" (a hidden credential
	// directory) or "network" (network access is off for the run).
	Kind string `json:"kind"`
	// Path is the path the denial names, when there is one.
	Path string `json:"path,omitempty"`
	// SuggestedRoot is a directory the user could add as a writable workspace
	// root so the command can write Path. It is set only for a write the
	// command itself named (its text contains the path), so output a command
	// prints cannot steer the proposal, and never for a directory that holds
	// the home directory, a credential directory or a system top-level
	// directory.
	SuggestedRoot string `json:"suggested_root,omitempty"`
}

// Hint is the note added to the tool result the model reads.
func (d *Denial) Hint() string {
	switch d.Kind {
	case "write":
		hint := fmt.Sprintf("[sandbox] Writing %s was blocked: it is outside the writable workspace roots.", d.Path)
		if d.SuggestedRoot != "" {
			hint += fmt.Sprintf(" Work inside the workspace, or ask the user to add %s as a writable workspace root.", d.SuggestedRoot)
		} else {
			hint += " Work inside the workspace or a temporary directory."
		}
		return hint + " Do not retry with sudo."
	case "protected":
		return fmt.Sprintf("[sandbox] %s is read-only in the sandbox (git hooks, configuration and the files that redirect them, agent settings and configured read-only paths are protected). Ask the user to make this change outside the sandbox.", d.Path)
	case "masked":
		return fmt.Sprintf("[sandbox] %s is hidden from sandboxed commands because it holds credentials.", d.Path)
	case "network":
		return "[sandbox] Network access is off for this run. Ask the user to allow network access if the command needs it."
	}
	return ""
}

// denialMarkers are the error texts a denied file operation produces: EACCES
// under Seatbelt and Landlock, EPERM and EROFS under bubblewrap's read-only
// mounts, and EBUSY for a rename over a file bubblewrap bound in place (git
// writes its config that way), in the spellings of the C library, Go, Node and
// Python, and ERROR_ACCESS_DENIED under the Windows low-integrity sandbox.
var denialMarkers = []string{"operation not permitted", "permission denied", "read-only file system", "resource busy", "eacces", "eperm", "erofs", "ebusy", "access is denied"}

// networkMarkers are resolver and connect failures an offline run produces.
var networkMarkers = []string{
	"network is unreachable",
	"could not resolve host",
	"temporary failure in name resolution",
	"name or service not known",
	"nodename nor servname provided",
	"no address associated with hostname",
	"getaddrinfo",
	"enotfound",
	"eai_again",
	"failed to resolve",
	"dial tcp",
}

const denialScanBytes = 64 << 10

// ExplainFailure recognizes a sandbox denial in the output of a command that
// exited with exitCode under p. It returns nil when the command succeeded,
// could not be found, or failed for a reason the sandbox does not explain,
// such as a permission error inside a writable root.
func (p *Policy) ExplainFailure(command, dir, output string, exitCode *int) *Denial {
	if !p.Enabled() || exitCode == nil || *exitCode == 0 || *exitCode == 127 {
		return nil
	}
	root, err := canonicalPath(p.Root)
	if err != nil {
		return nil
	}
	if dir == "" {
		dir = root
	}
	if len(output) > denialScanBytes {
		output = output[len(output)-denialScanBytes:]
	}
	writable, _ := resolveAll(root, p.Writable)
	masked, _ := resolveAll(root, p.Masked)
	protected, _ := resolveAll(root, append(append([]string(nil), p.ReadOnly...), p.Protected...))
	grants := append(append([]string{root}, writable...), commandTempDirs(root)...)
	network := false
	for _, line := range strings.Split(output, "\n") {
		lower := strings.ToLower(line)
		if !p.Network && containsAny(lower, networkMarkers) {
			network = true
		}
		at := markerIndex(lower, denialMarkers)
		if at < 0 {
			continue
		}
		for _, token := range pathTokens(line, at) {
			if d := classifyDenied(token, dir, command, grants, masked, protected); d != nil {
				return d
			}
		}
	}
	if network {
		return &Denial{Kind: "network"}
	}
	return nil
}

func classifyDenied(token, dir, command string, grants, masked, protected []string) *Denial {
	path := token
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		path = filepath.Join(homeDir(), rest)
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	resolved, err := canonicalPath(path)
	if err != nil {
		return nil
	}
	switch {
	case enclosedByAny(masked, resolved):
		return &Denial{Kind: "masked", Path: resolved}
	case enclosedByAny(protected, resolved):
		return &Denial{Kind: "protected", Path: resolved}
	case enclosedByAny(grants, resolved):
		// Inside a writable root: an ordinary permission error.
		return nil
	case !hostWritable(resolved):
		// The user could not write it outside the sandbox either (/etc, a
		// file of another user, or a read the host itself refuses).
		return nil
	}
	d := &Denial{Kind: "write", Path: resolved}
	if strings.Contains(command, token) {
		d.SuggestedRoot = suggestRoot(resolved, masked)
	}
	return d
}

// suggestRoot proposes the nearest existing directory of path, unless that
// would expose more than the write needs.
func suggestRoot(path string, masked []string) string {
	dir := path
	for {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	// A system top-level directory (/etc, /usr) or the filesystem root is
	// never a workspace.
	if strings.Count(strings.Trim(filepath.ToSlash(dir), "/"), "/") < 1 {
		return ""
	}
	if home := homeDir(); home != "" && encloses(dir, home) {
		return ""
	}
	if checkRoot(dir, masked) != nil {
		return ""
	}
	return dir
}

// pathTokens returns the words of an error line that look like paths (they
// contain a path separator or start with a dot or a tilde), nearest the denial marker at
// index at first: "sh: 1: cannot create /x: Permission denied" names the
// shell before the path it was refused, and Python names the path after.
func pathTokens(line string, at int) []string {
	var before, after []string
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		// "open /x: ..." puts a colon right after the path.
		field := strings.TrimRight(line[start:end], ":;.")
		if field != "" && !strings.Contains(field, "://") &&
			(strings.ContainsAny(field, `/`+string(filepath.Separator)) || strings.HasPrefix(field, ".") || strings.HasPrefix(field, "~")) {
			if start < at {
				before = append(before, field)
			} else {
				after = append(after, field)
			}
		}
		start = -1
	}
	for i, r := range line {
		switch r {
		case ' ', '\t', '\'', '"', '`', '(', ')', ',', '[', ']', '{', '}', '<', '>', '=':
			flush(i)
		default:
			if start < 0 {
				start = i
			}
		}
	}
	flush(len(line))
	slices.Reverse(before)
	return append(before, after...)
}

func markerIndex(s string, markers []string) int {
	at := -1
	for _, marker := range markers {
		if i := strings.Index(s, marker); i >= 0 && (at < 0 || i < at) {
			at = i
		}
	}
	return at
}

func containsAny(s string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func enclosedByAny(dirs []string, path string) bool {
	for _, dir := range dirs {
		if encloses(dir, path) {
			return true
		}
	}
	return false
}
