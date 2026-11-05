//go:build windows

package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// The native Windows sandbox runs each command at low integrity, under a copy
// of the user's own token with its privileges removed. Mandatory integrity
// control stops a low-integrity process from writing, deleting or renaming any
// medium-integrity object (an unlabelled object is medium), which covers the
// user's files, the registry under HKCU (except the low-integrity AppDataLow
// key) and the user's other processes. The writable roots and a fenced temp
// directory are labelled low, and protected paths inside them medium, so the
// command can write the roots and nothing else. Masked directories are
// labelled medium with no-read-up, so the command cannot read them either;
// everything else stays readable. The network is untouched: the token keeps
// the user's logon session, so TLS clients that rely on the system's
// credential providers keep working.
//
// The labels stay after the run and are reused by the next run on the same
// root; setting them again is a no-op. Removing them on exit would race with
// concurrent runs on the same root.

const windowsMechanism = "low-integrity"

// windowsLimitations is reported with a supported capability.
const windowsLimitations = "without `kn sandbox setup` (administrator approval required) the native Windows sandbox cannot block network access, and other low-integrity processes can write directories once used as sandbox workspaces; run the setup, or run K-brain inside WSL2"

// windowsNoReadMasking replaces windowsLimitations when the probe finds that a
// no-read-up label does not hide a file from a low-integrity process.
const windowsNoReadMasking = "without `kn sandbox setup` (administrator approval required) the native Windows sandbox cannot block network access or hide credential directories from reads here, and other low-integrity processes can write directories once used as sandbox workspaces; run the setup, or run K-brain inside WSL2"

func probeWindows() Capability {
	forgetSandboxState()
	err := probeSandboxUser()
	if err == nil {
		setSandboxUserProblem("")
		return sandboxUserCapability()
	}
	c := probeLowIntegrityCapability()
	c.SetupAvailable = true
	c.SetupHint = setupHint
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		setSandboxUserProblem(err.Error())
		c.SetupHint = repairHint(err.Error())
	}
	return c
}

func probeLowIntegrityCapability() Capability {
	c := Capability{Mechanism: windowsMechanism, Platform: "windows"}
	masking, err := probeLowIntegrity()
	if err != nil {
		c.Reason = "the low-integrity sandbox cannot start here: " + err.Error()
		return c
	}
	c.Supported, c.ReadMasking, c.PathProtection = true, masking, true
	c.Reason = windowsLimitations
	if !masking {
		c.Reason = windowsNoReadMasking
	}
	return c
}

// probeLowIntegrity confines a trivial cmd.exe and checks that it can write a
// low-labelled directory and not a sibling. It also reports whether the
// command can read a file in a masked directory, which this process can.
func probeLowIntegrity() (readMasking bool, err error) {
	base, err := os.MkdirTemp("", "k-brain-sandbox-probe-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(base)
	inside, outside, masked := filepath.Join(base, "inside"), filepath.Join(base, "outside"), filepath.Join(base, "masked")
	for _, dir := range []string{inside, outside, masked} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return false, err
		}
	}
	secret := filepath.Join(masked, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		return false, err
	}
	if err := ensureLabel(inside, writableLabel); err != nil {
		return false, err
	}
	if err := ensureMasked(masked); err != nil {
		return false, err
	}
	if _, err := os.ReadFile(secret); err != nil {
		return false, fmt.Errorf("a masked file is unreadable outside the sandbox: %w", err)
	}
	cmdExe := filepath.Join(systemRoot(), "System32", "cmd.exe")
	null, err := openNull()
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(null)
	_, err = runConfined(confinedProcess{
		program: cmdExe,
		cmdLine: syscall.EscapeArg(cmdExe) + ` /d /s /c "echo ok>probe.txt& echo ok>..\outside\probe.txt& type ..\masked\secret.txt>read.txt"`,
		dir:     inside,
		env:     os.Environ(),
		stdin:   null, stdout: null, stderr: null,
	})
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(filepath.Join(inside, "probe.txt")); err != nil {
		return false, fmt.Errorf("a confined command could not write its workspace: %w", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "probe.txt")); err == nil {
		return false, errors.New("a confined command could write outside its workspace")
	}
	read, err := os.ReadFile(filepath.Join(inside, "read.txt"))
	if err != nil {
		return false, fmt.Errorf("a confined command could not write its workspace: %w", err)
	}
	return !strings.Contains(string(read), "secret"), nil
}

// wrapWindows labels the policy's paths and returns a command that re-executes
// this binary as the launcher (windows_launch.go), which starts the real
// command at low integrity in a job object.
func (p *Policy) wrapWindows(ctx context.Context, cmd *exec.Cmd, root string) (*exec.Cmd, error) {
	switch backend := strings.ToLower(strings.TrimSpace(p.Backend)); backend {
	case "", "auto", MechanismSandboxUser:
		if Probe().Mechanism == MechanismSandboxUser {
			return p.wrapSandboxUser(ctx, cmd, root)
		}
		if backend == MechanismSandboxUser {
			return nil, errors.New("sandbox backend sandbox-user needs `kn sandbox setup` (administrator approval required); run it, or use backend auto")
		}
	case windowsMechanism:
	case "wsl", "bwrap", "bubblewrap":
		return nil, fmt.Errorf("sandbox backend %q cannot confine native Windows programs; run K-brain inside WSL2 instead, or use backend auto", p.Backend)
	default:
		return nil, fmt.Errorf("unsupported Windows sandbox backend %q; use auto", p.Backend)
	}
	if !p.Network {
		return nil, errors.New(offlineUnavailable)
	}
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	dir := commandDir(cmd, root)
	program, err := windowsProgram(cmd.Path, dir)
	if err != nil {
		return nil, err
	}
	plan, err := p.prepareWindows(root)
	if err != nil {
		return nil, err
	}
	cmdLine := ""
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.CmdLine != "" {
		cmdLine = cmd.SysProcAttr.CmdLine
	} else {
		args := cmd.Args
		if len(args) == 0 {
			args = []string{program}
		}
		cmdLine = composeCommandLine(args)
	}
	spec, err := windowsLaunchSpec{Program: program, CmdLine: cmdLine, Dir: dir, Temp: plan.temp, Pins: plan.pins}.encode()
	if err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("sandbox launcher: %w", err)
	}
	wrapped := exec.CommandContext(ctx, self, launchMarker, spec)
	wrapped.Env = cmd.Env
	wrapped.Dir = dir
	return wrapped, nil
}

type windowsPlan struct {
	temp string
	// pins are the protected paths inside a writable root and their ancestors
	// below it. A low-integrity command can rename or delete a medium-labelled
	// child of a low-labelled directory (the parent grants the deletion), so
	// the launcher holds them open without delete sharing while the command
	// runs, which makes every rename or delete of them fail.
	pins []string
}

// prepareWindows labels the writable roots and the fenced temp directory low,
// the protected and read-only paths inside the roots medium, and the masked
// directories medium with no-read-up. Masked directories are outside every
// writable root (checkRoot refuses a root that encloses one). A root inside a
// masked directory (the default project of a client usually lives in its data
// directory) keeps its low label: it is labelled first, and the mask's walk
// leaves explicitly low subtrees alone, much as the other platforms bind the
// root back inside the mask.
func (p *Policy) prepareWindows(root string) (windowsPlan, error) {
	roots := []string{root}
	writable, err := resolveAll(root, p.Writable)
	if err != nil {
		return windowsPlan{}, err
	}
	for _, path := range writable {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := checkRoot(path, p.Masked); err != nil {
			return windowsPlan{}, err
		}
		roots = append(roots, path)
	}
	for _, r := range roots {
		if err := ensureLabel(r, writableLabel); err != nil {
			return windowsPlan{}, err
		}
	}
	protected, err := resolveAll(root, append(append([]string(nil), p.ReadOnly...), p.Protected...))
	if err != nil {
		return windowsPlan{}, err
	}
	var pins []string
	pin := func(path string) {
		if !slices.Contains(pins, path) {
			pins = append(pins, path)
		}
	}
	for _, path := range protected {
		if _, err := os.Lstat(path); err != nil {
			// A protected path that does not exist yet has no object to carry a
			// label; it is left to the root's label, as with bubblewrap.
			continue
		}
		if isNameSurrogate(path) {
			return windowsPlan{}, fmt.Errorf("sandbox refuses protected path %q: it is a link; remove the link or choose another workspace", path)
		}
		for _, writableRoot := range roots {
			if !encloses(writableRoot, path) || writableRoot == path {
				continue
			}
			if err := ensureLabel(path, protectedLabel); err != nil {
				return windowsPlan{}, err
			}
			// Renaming an ancestor inside the root would move the protected path
			// aside and let the command recreate it, so the ancestors are pinned
			// as well.
			for dir := path; dir != writableRoot && encloses(writableRoot, dir); dir = filepath.Dir(dir) {
				pin(dir)
			}
		}
	}
	temp, err := fencedTemp(root)
	if err != nil {
		return windowsPlan{}, err
	}
	if err := p.prepareMasks(); err != nil {
		return windowsPlan{}, err
	}
	return windowsPlan{temp: temp, pins: pins}, nil
}

// prepareMasks labels the existing masked directories (after resolving links)
// medium with no-read-up, outermost first. A masked path that
// does not exist has nothing to hide. Masking a drive root, the profile or a
// system directory is refused, as is anything not on a local drive, because
// such a label would hide far more than credentials or cannot be set.
func (p *Policy) prepareMasks() error {
	var masked []string
	for _, path := range p.Masked {
		if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
			continue
		}
		resolved, err := canonicalPath(path)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(resolved); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("sandbox cannot hide %s: %w", resolved, err)
		}
		if err := checkMaskable(resolved); err != nil {
			return err
		}
		if !slices.Contains(masked, resolved) {
			masked = append(masked, resolved)
		}
	}
	var outer []string
	for _, m := range masked {
		if !slices.ContainsFunc(masked, func(other string) bool { return other != m && encloses(other, m) }) {
			outer = append(outer, m)
		}
	}
	for _, m := range outer {
		if err := ensureMasked(m); err != nil {
			return fmt.Errorf("sandbox cannot hide %s from sandboxed commands: %w", m, err)
		}
	}
	return nil
}

func checkMaskable(path string) error {
	refuse := func(why string) error {
		return fmt.Errorf("sandbox refuses to mask %q: %s", path, why)
	}
	vol := filepath.VolumeName(path)
	if len(vol) != 2 || vol[1] != ':' {
		return refuse("only directories on local drives can be masked")
	}
	if strings.Trim(strings.TrimPrefix(path, vol), `\/`) == "" {
		return refuse("it is a drive root")
	}
	if home := homeDir(); home != "" && encloses(path, home) {
		return refuse("it is the user profile or contains it")
	}
	for _, env := range []string{"SystemRoot", "windir", "ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "ProgramData"} {
		if dir := strings.TrimSpace(os.Getenv(env)); dir != "" && filepath.IsAbs(dir) && encloses(path, dir) {
			return refuse("it contains a system directory")
		}
	}
	if isNameSurrogate(path) {
		return refuse("it is a link or junction")
	}
	return nil
}

// fencedTempPath is where fencedTemp puts the temporary directory of root.
func fencedTempPath(root string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(root))))
	return filepath.Join(os.TempDir(), "k-brain-sandbox", hex.EncodeToString(sum[:6]))
}

// commandTempDirs are the temporary directories a confined command may write:
// only the fenced one, not the system temp directory.
func commandTempDirs(root string) []string { return []string{fencedTempPath(root)} }

// fencedTemp is the run's temporary directory: one per workspace under the
// user's temp directory, labelled low. Its parent is created by K-brain and
// stays medium, so a sandboxed command cannot replace the directory with a
// link to somewhere else.
func fencedTemp(root string) (string, error) {
	dir := fencedTempPath(root)
	parent := filepath.Dir(dir)
	for _, path := range []string{parent, dir} {
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", err
		}
		if info, err := os.Lstat(path); err != nil || !info.IsDir() || isNameSurrogate(path) {
			return "", fmt.Errorf("sandbox temp directory %s is not a plain directory", path)
		}
	}
	if l, _, _, err := fileLabel(parent); err != nil || !l.satisfies(protectedLabel) {
		return "", fmt.Errorf("sandbox temp directory %s is writable at low integrity; remove it and retry", parent)
	}
	if err := ensureLabel(dir, writableLabel); err != nil {
		return "", err
	}
	return dir, nil
}

// checkPlatformRoot refuses roots whose write grant is unsafe on Windows: drive
// roots, UNC and device paths, the user profile or a directory containing it,
// the Windows and program directories, and paths through links or junctions
// (a link could be retargeted between runs to redirect the grant).
func checkPlatformRoot(root string) error {
	refuse := func(why string) error {
		return fmt.Errorf("sandbox refuses workspace %q: %s; choose a project directory", root, why)
	}
	vol := filepath.VolumeName(root)
	if len(vol) != 2 || vol[1] != ':' {
		return refuse("network (UNC) and device paths are not supported")
	}
	rest := strings.Trim(strings.TrimPrefix(root, vol), `\/`)
	if rest == "" {
		return refuse("it is a drive root")
	}
	if home := homeDir(); home != "" && encloses(root, home) {
		return refuse("it is the user profile or contains it")
	}
	for _, env := range []string{"SystemRoot", "windir", "ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "ProgramData"} {
		dir := strings.TrimSpace(os.Getenv(env))
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		if encloses(root, dir) || encloses(dir, root) {
			return refuse("it is a system directory")
		}
	}
	cur := vol + `\`
	for _, part := range strings.Split(rest, `\`) {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		if isNameSurrogate(cur) {
			return refuse(fmt.Sprintf("%s is a link or junction", cur))
		}
	}
	return nil
}

// windowsProgram returns the absolute program to start. A bare name is looked
// up only in absolute PATH directories with PATHEXT, never in the working
// directory, so a binary planted in the workspace cannot stand in for a tool.
// App execution aliases under WindowsApps cannot start at low integrity and
// are refused with a hint.
func windowsProgram(path, dir string) (string, error) {
	if path == "" {
		return "", errors.New("sandbox: empty program")
	}
	program := path
	switch {
	case filepath.IsAbs(path):
	case strings.ContainsAny(path, `\/`) || filepath.VolumeName(path) != "":
		program = filepath.Join(dir, path)
	default:
		found, err := lookPathAbsolute(path)
		if err != nil {
			return "", err
		}
		program = found
	}
	if strings.Contains(strings.ToLower(filepath.ToSlash(program)), "/microsoft/windowsapps/") {
		return "", fmt.Errorf("sandbox cannot start %s: Microsoft Store app aliases do not start at low integrity; install the program outside the Store or put a regular install first in PATH", program)
	}
	return program, nil
}

func lookPathAbsolute(name string) (string, error) {
	exts := []string{""}
	if filepath.Ext(name) == "" {
		exts = nil
		for _, ext := range strings.Split(os.Getenv("PATHEXT"), ";") {
			if ext = strings.TrimSpace(ext); ext != "" {
				exts = append(exts, ext)
			}
		}
		if len(exts) == 0 {
			exts = []string{".com", ".exe", ".bat", ".cmd"}
		}
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		dir = strings.Trim(dir, `"`)
		if !filepath.IsAbs(dir) {
			continue
		}
		for _, ext := range exts {
			candidate := filepath.Join(dir, name+ext)
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("sandbox: %s not found in PATH", name)
}

// composeCommandLine quotes args the way os/exec does.
func composeCommandLine(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = syscall.EscapeArg(arg)
	}
	return strings.Join(quoted, " ")
}

func systemRoot() string {
	if dir := os.Getenv("SystemRoot"); filepath.IsAbs(dir) {
		return dir
	}
	return `C:\Windows`
}

// windowsLaunchSpec is what the launcher needs to start the confined command.
type windowsLaunchSpec struct {
	Program string   `json:"program"`
	CmdLine string   `json:"cmdline"`
	Dir     string   `json:"dir"`
	Temp    string   `json:"temp"`
	Pins    []string `json:"pins,omitempty"`
}

func (s windowsLaunchSpec) encode() (string, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeWindowsLaunchSpec(text string) (windowsLaunchSpec, error) {
	var s windowsLaunchSpec
	data, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, err
	}
	if !filepath.IsAbs(s.Program) || !filepath.IsAbs(s.Dir) || !filepath.IsAbs(s.Temp) {
		return s, errors.New("incomplete launch specification")
	}
	return s, nil
}
