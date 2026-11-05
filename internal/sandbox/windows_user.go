//go:build windows

package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The sandbox-user level runs each command as one of two hidden standard
// accounts created by `kn sandbox setup`: the online account for runs
// that may use the network, the offline account, whose every connection the
// filtering platform blocks (windows_wfp.go), for the others. The user's files
// grant those accounts nothing, so the integrity level stays medium and
// nothing is labelled low: the writable roots, the fenced temp directory and
// the tools the command needs from the user's profile get access entries for
// the sandbox group (windows_acl.go), protected paths a deny of writing, and
// masked directories a deny of everything.
//
// Write access is per workspace for offline runs. Their command runs under a
// restricted token whose restricting SIDs carry one synthetic SID per writable
// root of the run (setupNames.workspaceSID); a root grants write to the group
// and to its own SID, and a restricted token needs both, so an offline command
// cannot write the workspaces of other runs even though the group can. Online
// runs cannot use a restricted token: the system's TLS provider refuses
// credentials to any restricted token, so TLS clients that use it (curl.exe,
// PowerShell, .NET, Git with Schannel) would lose HTTPS. Their command runs
// under the online account's token (privileges removed), which can write every workspace a
// sandboxed run has used, but nothing of the user's beyond them, and no
// process outside the sandbox can either: the grants name only the sandbox
// accounts.
//
// Starting the accounts needs their password and a new logon, which only
// CreateProcessWithLogonW gives a process without privileges. Its command
// line is limited and it takes no attribute list, so the launch has two
// stages: the user's launcher (stage one) holds the protected paths open,
// logs the account on with this binary as stage two, suspended, puts it into
// a kill-on-close job and lets it run; stage two, now the sandbox account,
// hardens its own process, derives the command's token and starts the
// command as the low-integrity launcher does (runConfined), inside the same
// job. Stage two runs on the launcher's desktop, the only one the secondary
// logon service lets it start on; the command runs on a private desktop
// that stage one creates. The account's token carries the launching user's
// logon SID (the service adds it); the command's token keeps it deny-only,
// so that the command reaches neither the user's desktop nor the objects of
// the user's processes.

const (
	userLaunchMarker = "__k-brain-sandbox-user-launch__"
	userStageMarker  = "__k-brain-sandbox-user-stage__"
	selfTestMarker   = "__k-brain-sandbox-user-selftest__"
	// stageSpecEnv carries stage two's specification in its environment:
	// CreateProcessWithLogonW limits the command line to 1024 characters.
	stageSpecEnv = "K_BRAIN_SANDBOX_STAGE"

	logonWithNoProfile  = 0
	startfUseShowWindow = 0x1
	seGroupLogonID      = 0xC0000000
	disableMaxPrivilege = 0x1
	// winstaSandboxAccess is WINSTA_ALL_ACCESS, what the logon session has
	// and what a process needs to connect to a desktop of the station; the
	// job's UI restrictions keep the command from the clipboard, global
	// atoms, new desktops and logging off.
	winstaSandboxAccess   = 0x37f
	desktopGenericAll     = 0x10000000
	th32csSnapThread      = 0x4
	tokenAdjustDefaultDCL = windows.TOKEN_ADJUST_DEFAULT
)

var (
	procCreateProcessWithLogonW = modadvapi32.NewProc("CreateProcessWithLogonW")
	procCreateRestrictedToken   = modadvapi32.NewProc("CreateRestrictedToken")
	procCreateDesktopW          = moduser32.NewProc("CreateDesktopW")
	procCloseDesktop            = moduser32.NewProc("CloseDesktop")
	procOpenWindowStationW      = moduser32.NewProc("OpenWindowStationW")
	procCloseWindowStation      = moduser32.NewProc("CloseWindowStation")
)

func init() {
	if len(os.Args) == 3 && os.Args[1] == userLaunchMarker {
		os.Exit(runUserLauncher(os.Args[2]))
	}
	if len(os.Args) == 2 && os.Args[1] == userStageMarker {
		os.Exit(runUserStage())
	}
	if len(os.Args) == 6 && os.Args[1] == selfTestMarker {
		os.Exit(runSelfTest(os.Args[2:]))
	}
}

// sandboxUserSpec is stage one's specification.
type sandboxUserSpec struct {
	windowsLaunchSpec
	Offline   bool   `json:"offline"`
	StatePath string `json:"state"`
	// Restrict are the restricting SIDs of an offline command beyond the
	// fixed ones: the reader SID, a SID per writable root and the offline
	// group.
	Restrict []string `json:"restrict,omitempty"`
	// SafeDirs are the roots Git may treat as safe although another user
	// (the user, not the sandbox account) owns them.
	SafeDirs []string `json:"safe_dirs,omitempty"`
}

// stageSpec is stage two's specification.
type stageSpec struct {
	Program  string   `json:"program"`
	CmdLine  string   `json:"cmdline"`
	Dir      string   `json:"dir"`
	Offline  bool     `json:"offline"`
	Restrict []string `json:"restrict,omitempty"`
	Desktop  string   `json:"desktop,omitempty"`
}

// traceEnv names a directory where both launch stages append progress
// lines; it is for diagnosing a launch that hangs.
const traceEnv = "K_BRAIN_SANDBOX_TRACE"

func stageTrace(dir, stage, format string, args ...any) {
	if dir == "" || os.Getenv(traceEnv) == "" {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "trace-"+stage+".txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	fmt.Fprintf(f, time.Now().Format("15:04:05.000")+" "+format+"\n", args...)
	f.Close()
}

func encodeJSON(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeJSON(text string, v any) error {
	data, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// sandboxUserProblem is why the last probe of the sandbox-user level failed.
var (
	sandboxUserProblemMu sync.Mutex
	sandboxUserProblem   string
)

func lastSandboxUserProblem() string {
	sandboxUserProblemMu.Lock()
	defer sandboxUserProblemMu.Unlock()
	return sandboxUserProblem
}

func setSandboxUserProblem(p string) {
	sandboxUserProblemMu.Lock()
	sandboxUserProblem = p
	sandboxUserProblemMu.Unlock()
}

// sandboxUserLimitations is reported with the capability of a working setup.
const sandboxUserLimitations = "commands run as dedicated sandbox accounts; offline runs cannot reach the network or loopback (name lookups by the system's DNS client excepted), online runs can write every workspace a sandboxed run has used"

// sandboxUserCapability is the capability of a working setup.
func sandboxUserCapability() Capability {
	return Capability{Supported: true, Mechanism: MechanismSandboxUser, Platform: "windows", NetworkControl: true, ReadMasking: true, PathProtection: true, SetupAvailable: true,
		Reason: sandboxUserLimitations}
}

// probeSandboxUser runs the self test as both accounts in a scratch
// workspace: the offline account must write the workspace, fail to write
// outside it, fail to read a masked file and fail to reach a loopback
// listener; the online account must write the workspace and reach it.
func probeSandboxUser() error {
	st, err := cachedSandboxState()
	if err != nil {
		return err
	}
	base, err := os.MkdirTemp("", "k-brain-sandbox-user-probe-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(base)
	if base, err = canonicalPath(base); err != nil {
		return err
	}
	inside, outside, masked := filepath.Join(base, "inside"), filepath.Join(base, "outside"), filepath.Join(base, "masked")
	for _, dir := range []string{inside, outside, masked} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
	}
	secret := filepath.Join(masked, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	self, err := os.Executable()
	if err != nil {
		return err
	}
	var problems []error
	for _, offline := range []bool{true, false} {
		p := &Policy{Mode: "workspace", Root: inside, Network: !offline, Masked: []string{masked}}
		mode := "online"
		if offline {
			mode = "offline"
		}
		cmd := exec.Command(self, selfTestMarker, mode, filepath.Join(outside, "probe-"+mode+".txt"), secret, ln.Addr().String())
		cmd.Dir = inside
		cmd.Env = os.Environ()
		code, out, err := runProbeCommand(p, st, cmd, inside)
		if err != nil {
			problems = append(problems, fmt.Errorf("the %s sandbox account cannot run commands: %w", mode, err))
		} else if code != 0 {
			problems = append(problems, fmt.Errorf("the %s sandbox account failed its self test (%s)", mode, selfTestProblem(code, out)))
		}
	}
	return errors.Join(problems...)
}

func runProbeCommand(p *Policy, st *setupState, cmd *exec.Cmd, root string) (int, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	wrapped, err := p.wrapSandboxUserWith(ctx, cmd, root, st, false)
	if err != nil {
		return 0, "", err
	}
	out, err := wrapped.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return 0, string(out), err
		}
		if exitErr.ExitCode() == launcherFailureCode {
			return 0, "", errors.New(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "k-brain sandbox:")))
		}
		return exitErr.ExitCode(), string(out), nil
	}
	return 0, string(out), nil
}

const (
	selfTestWriteFailed = 1 << iota
	selfTestWroteOutside
	selfTestReadMasked
	selfTestNetwork
)

func selfTestProblem(code int, out string) string {
	var problems []string
	if code&selfTestWriteFailed != 0 {
		problems = append(problems, "it could not write its workspace")
	}
	if code&selfTestWroteOutside != 0 {
		problems = append(problems, "it could write outside its workspace")
	}
	if code&selfTestReadMasked != 0 {
		problems = append(problems, "it could read a masked file")
	}
	if code&selfTestNetwork != 0 {
		problems = append(problems, "its network access is not as configured")
	}
	if len(problems) == 0 {
		problems = append(problems, fmt.Sprintf("exit code %d", code))
	}
	if detail := strings.Join(strings.Fields(out), " "); detail != "" {
		problems = append(problems, detail)
	}
	return strings.Join(problems, "; ")
}

// runSelfTest is the confined side of probeSandboxUser.
func runSelfTest(args []string) int {
	mode, outside, secret, addr := args[0], args[1], args[2], args[3]
	code := 0
	if err := os.WriteFile("probe.txt", []byte("ok"), 0o600); err != nil {
		fmt.Printf("write: %v.", err)
		code |= selfTestWriteFailed
	}
	if os.WriteFile(outside, []byte("escaped"), 0o600) == nil {
		code |= selfTestWroteOutside
	}
	if data, err := os.ReadFile(secret); err == nil && len(data) > 0 {
		code |= selfTestReadMasked
	}
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err == nil {
		c.Close()
	}
	if (mode == "offline") == (err == nil) {
		fmt.Printf("dial: %v.", err)
		code |= selfTestNetwork
	}
	return code
}

// userPlan is what prepareSandboxUser set up for one command.
type userPlan struct {
	temp     string
	pins     []string
	restrict []string
	roots    []string
}

// readerGrants are the paths outside the workspaces that every sandboxed
// command may read, granted once per process.
var (
	readerGrantMu   sync.Mutex
	readerGranted   = map[string]bool{}
	readerPathsDone bool
)

// prepareSandboxUser gives the sandbox group and the run's workspace SIDs
// write access to the writable roots and the fenced temp directory, denies
// writing the protected and read-only paths inside the roots, denies
// everything on the masked directories and lets the group read the program,
// this binary and the PATH directories inside the profile. Every changed path
// is recorded for uninstall when record is set (the probe's scratch paths are
// removed instead).
func (p *Policy) prepareSandboxUser(root, program string, st *setupState, record bool) (userPlan, error) {
	n := st.names
	ours := n.sandboxACE([]string{st.Group.SID, st.OfflineGroup.SID})
	note := func(path string) error {
		if !record {
			return nil
		}
		return recordACLRoot(st, path)
	}
	roots := []string{root}
	writable, err := resolveAll(root, p.Writable)
	if err != nil {
		return userPlan{}, err
	}
	for _, path := range writable {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := checkRoot(path, p.Masked); err != nil {
			return userPlan{}, err
		}
		if !slices.Contains(roots, path) {
			roots = append(roots, path)
		}
	}
	plan := userPlan{restrict: []string{n.readerSID()}, roots: roots}
	grantRoot := func(path, sid string) error {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		t, err := grantTarget(accessAllowedACEType, fileModify, true, info.IsDir(), st.Group.SID, sid)
		if err != nil {
			return err
		}
		if err := note(path); err != nil {
			return err
		}
		return applyTree(path, t, aclWalkGrant, ours, false)
	}
	for _, r := range roots {
		sid := n.workspaceSID(r)
		if err := grantRoot(r, sid); err != nil {
			return userPlan{}, fmt.Errorf("sandbox cannot grant the workspace %s: %w", r, err)
		}
		plan.restrict = appendNew(plan.restrict, sid)
	}
	protected, err := resolveAll(root, append(append([]string(nil), p.ReadOnly...), p.Protected...))
	if err != nil {
		return userPlan{}, err
	}
	for _, path := range protected {
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}
		if isNameSurrogate(path) {
			return userPlan{}, fmt.Errorf("sandbox refuses protected path %q: it is a link; remove the link or choose another workspace", path)
		}
		for _, r := range roots {
			if !encloses(r, path) || r == path {
				continue
			}
			t, err := grantTarget(accessDeniedACEType, fileWriteRights, false, info.IsDir(), st.Group.SID)
			if err != nil {
				return userPlan{}, err
			}
			if err := note(path); err != nil {
				return userPlan{}, err
			}
			if err := applyTree(path, t, aclWalkDeny, ours, false); err != nil {
				return userPlan{}, fmt.Errorf("sandbox cannot protect %s: %w", path, err)
			}
			for dir := path; dir != r && encloses(r, dir); dir = filepath.Dir(dir) {
				plan.pins = appendNew(plan.pins, dir)
			}
		}
	}
	temp := fencedTempPath(root)
	for _, dir := range []string{filepath.Dir(temp), temp} {
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return userPlan{}, err
		}
		if info, err := os.Lstat(dir); err != nil || !info.IsDir() || isNameSurrogate(dir) {
			return userPlan{}, fmt.Errorf("sandbox temp directory %s is not a plain directory", dir)
		}
	}
	if err := grantRoot(temp, n.workspaceSID(root)); err != nil {
		return userPlan{}, fmt.Errorf("sandbox cannot grant its temp directory %s: %w", temp, err)
	}
	plan.temp = temp
	if err := p.denyMasks(st, ours, note); err != nil {
		return userPlan{}, err
	}
	// Reader grants are made once per process and outlive the probe's
	// scratch paths, so they are always recorded.
	if err := grantReaders(st, ours, program, roots, func(path string) error { return recordACLRoot(st, path) }); err != nil {
		return userPlan{}, err
	}
	if !p.Network {
		plan.restrict = appendNew(plan.restrict, st.OfflineGroup.SID)
	}
	return plan, nil
}

// denyMasks denies the sandbox group every access to the existing masked
// directories. A workspace inside a masked directory keeps its grant: the
// walk leaves subtrees with an explicit grant alone. Trees are walked again
// once maskRecheck has passed, for files moved in with their own access.
func (p *Policy) denyMasks(st *setupState, ours func(rawACE) bool, note func(string) error) error {
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
	for _, m := range masked {
		if slices.ContainsFunc(masked, func(other string) bool { return other != m && encloses(other, m) }) {
			continue
		}
		info, err := os.Lstat(m)
		if err != nil {
			return err
		}
		t, err := grantTarget(accessDeniedACEType, fileAll, false, info.IsDir(), st.Group.SID)
		if err != nil {
			return err
		}
		if err := note(m); err != nil {
			return err
		}
		aclMu.Lock()
		at, ok := sandboxMaskVerified[m]
		aclMu.Unlock()
		force := !ok || time.Since(at) >= maskRecheck
		if err := applyTree(m, t, aclWalkDeny, ours, force); err != nil {
			return fmt.Errorf("sandbox cannot hide %s from sandboxed commands: %w", m, err)
		}
		aclMu.Lock()
		sandboxMaskVerified[m] = time.Now()
		aclMu.Unlock()
	}
	return nil
}

// grantReaders lets the sandbox group (and the reader SID of restricted
// tokens) read and execute this binary, the program and the PATH
// directories inside the user's profile, where per-user tools live. Paths
// inside a writable root are already readable; masked directories, the data
// directory and the profile itself are never granted.
func grantReaders(st *setupState, ours func(rawACE) bool, program string, roots []string, note func(string) error) error {
	readerGrantMu.Lock()
	defer readerGrantMu.Unlock()
	home := homeDir()
	inProfile := func(path string) bool {
		return home != "" && encloses(home, path) && !strings.EqualFold(filepath.Clean(path), filepath.Clean(home))
	}
	inRoot := func(path string) bool {
		return slices.ContainsFunc(roots, func(r string) bool { return encloses(r, path) })
	}
	grant := func(path string, tolerant bool) error {
		key := strings.ToLower(path)
		if readerGranted[key] {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			if tolerant {
				return nil
			}
			return err
		}
		t, err := grantTarget(accessAllowedACEType, fileReadExecute, false, info.IsDir(), st.Group.SID, st.names.readerSID())
		if err != nil {
			return err
		}
		if err := note(path); err != nil {
			return err
		}
		if err := applyTree(path, t, aclWalkGrant, ours, false); err != nil && !tolerant {
			return fmt.Errorf("sandbox cannot let its accounts run %s: %w", path, err)
		}
		readerGranted[key] = true
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := grant(self, false); err != nil {
		return err
	}
	if program != "" && !inRoot(program) && (inProfile(program) || strings.EqualFold(program, self)) {
		if err := grant(program, false); err != nil {
			return err
		}
	}
	if readerPathsDone {
		return nil
	}
	readerPathsDone = true
	dataDir := filepath.Dir(setupStateDir())
	masks := MaskedDirs()
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		dir = strings.Trim(strings.TrimSpace(dir), `"`)
		if dir == "" || !filepath.IsAbs(dir) || !inProfile(dir) || inRoot(dir) {
			continue
		}
		resolved, err := canonicalPath(dir)
		if err != nil || !isDir(resolved) || isNameSurrogate(resolved) || !inProfile(resolved) {
			continue
		}
		if strings.Contains(strings.ToLower(filepath.ToSlash(resolved)), "/microsoft/windowsapps") {
			continue
		}
		if dataDir != "." && (encloses(dataDir, resolved) || encloses(resolved, dataDir)) {
			continue
		}
		if slices.ContainsFunc(masks, func(m string) bool { return encloses(m, resolved) || encloses(resolved, m) }) {
			continue
		}
		if err := grant(resolved, true); err != nil {
			return err
		}
	}
	return nil
}

// wrapSandboxUser confines cmd as the sandbox accounts.
func (p *Policy) wrapSandboxUser(ctx context.Context, cmd *exec.Cmd, root string) (*exec.Cmd, error) {
	st, err := cachedSandboxState()
	if err != nil {
		return nil, fmt.Errorf("sandbox user setup: %w", err)
	}
	return p.wrapSandboxUserWith(ctx, cmd, root, st, true)
}

func (p *Policy) wrapSandboxUserWith(ctx context.Context, cmd *exec.Cmd, root string, st *setupState, record bool) (*exec.Cmd, error) {
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	dir := commandDir(cmd, root)
	program, err := windowsProgram(cmd.Path, dir)
	if err != nil {
		return nil, err
	}
	plan, err := p.prepareSandboxUser(root, program, st, record)
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
	spec := sandboxUserSpec{
		windowsLaunchSpec: windowsLaunchSpec{Program: program, CmdLine: cmdLine, Dir: dir, Temp: plan.temp, Pins: plan.pins},
		Offline:           !p.Network,
		StatePath:         st.path,
		SafeDirs:          plan.roots,
	}
	if !p.Network {
		spec.Restrict = plan.restrict
	}
	encoded, err := encodeJSON(spec)
	if err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("sandbox launcher: %w", err)
	}
	wrapped := exec.CommandContext(ctx, self, userLaunchMarker, encoded)
	wrapped.Env = cmd.Env
	wrapped.Dir = dir
	return wrapped, nil
}

// runUserLauncher is stage one, run as the user.
func runUserLauncher(encoded string) int {
	code, err := userLaunch(encoded)
	if err != nil {
		fmt.Fprintf(os.Stderr, "k-brain sandbox: %v\n", err)
		return launcherFailureCode
	}
	return int(code)
}

func userLaunch(encoded string) (uint32, error) {
	var spec sandboxUserSpec
	if err := decodeJSON(encoded, &spec); err != nil {
		return 0, err
	}
	if !filepath.IsAbs(spec.Program) || !filepath.IsAbs(spec.Dir) || !filepath.IsAbs(spec.Temp) || !filepath.IsAbs(spec.StatePath) {
		return 0, errors.New("incomplete launch specification")
	}
	st, err := loadSetupStateAt(spec.StatePath)
	if err != nil {
		return 0, fmt.Errorf("sandbox user setup: %w", err)
	}
	account := st.Online
	if spec.Offline {
		account = st.Offline
	}
	password, err := st.password(account)
	if err != nil {
		return 0, err
	}
	pins, err := pinPaths(spec.Pins)
	if err != nil {
		return 0, err
	}
	defer func() {
		for _, h := range pins {
			_ = windows.CloseHandle(h)
		}
	}()
	stdio, err := launcherStdio()
	if err != nil {
		return 0, err
	}
	for _, h := range stdio {
		_ = windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT)
	}
	// The offline command's restricted token passes the second access
	// check through the offline group, which is one of its restricting SIDs.
	grantees := []string{account.SID}
	if spec.Offline {
		grantees = append(grantees, st.OfflineGroup.SID)
	}
	desktop, closeDesktop, err := privateDesktop(grantees)
	if err != nil {
		return 0, err
	}
	defer closeDesktop()
	stage, err := encodeJSON(stageSpec{Program: spec.Program, CmdLine: spec.CmdLine, Dir: spec.Dir, Offline: spec.Offline, Restrict: spec.Restrict, Desktop: desktop})
	if err != nil {
		return 0, err
	}
	env := confinedEnv(os.Environ(), spec.Temp)
	env = append(gitSafeDirectoryEnv(env, spec.SafeDirs), stageSpecEnv+"="+stage)
	block, err := environmentBlock(env)
	if err != nil {
		return 0, err
	}
	job, err := confinementJob()
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(job)

	self, err := os.Executable()
	if err != nil {
		return 0, err
	}
	// Stage two starts on the launcher's desktop, which the secondary logon
	// service grants it (it does not come up on a desktop named here); it
	// withdraws that grant and starts the command on the private desktop.
	si := windows.StartupInfo{
		Cb:         uint32(unsafe.Sizeof(windows.StartupInfo{})),
		Flags:      windows.STARTF_USESTDHANDLES | startfUseShowWindow,
		ShowWindow: windows.SW_HIDE,
		StdInput:   stdio[0],
		StdOutput:  stdio[1],
		StdErr:     stdio[2],
	}
	user, _ := windows.UTF16PtrFromString(account.Name)
	domain, _ := windows.UTF16PtrFromString(".")
	pw, _ := windows.UTF16PtrFromString(password)
	app, err := windows.UTF16PtrFromString(self)
	if err != nil {
		return 0, err
	}
	cmdLine, err := windows.UTF16FromString(composeCommandLine([]string{self, userStageMarker}))
	if err != nil {
		return 0, err
	}
	dir, err := windows.UTF16PtrFromString(spec.Dir)
	if err != nil {
		return 0, err
	}
	var pi windows.ProcessInformation
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_DEFAULT_ERROR_MODE)
	r, _, callErr := procCreateProcessWithLogonW.Call(
		uintptr(unsafe.Pointer(user)), uintptr(unsafe.Pointer(domain)), uintptr(unsafe.Pointer(pw)), logonWithNoProfile,
		uintptr(unsafe.Pointer(app)), uintptr(unsafe.Pointer(&cmdLine[0])),
		uintptr(flags), uintptr(unsafe.Pointer(&block[0])),
		uintptr(unsafe.Pointer(dir)), uintptr(unsafe.Pointer(&si)), uintptr(unsafe.Pointer(&pi)))
	if r == 0 {
		stageTrace(spec.Temp, "1", "logon failed: %v", callErr)
		return 0, fmt.Errorf("log on the sandbox account %s: %w", account.Name, callErr)
	}
	stageTrace(spec.Temp, "1", "started stage two pid %d desktop %q flags %#x", pi.ProcessId, desktop, flags)
	defer windows.CloseHandle(pi.Process)
	defer windows.CloseHandle(pi.Thread)
	if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		_ = windows.TerminateProcess(pi.Process, launcherFailureCode)
		return 0, fmt.Errorf("confine the sandbox account's process: %w", err)
	}
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		_ = windows.TerminateProcess(pi.Process, launcherFailureCode)
		return 0, err
	}
	stageTrace(spec.Temp, "1", "resumed stage two")
	if _, err := windows.WaitForSingleObject(pi.Process, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil {
		return 0, err
	}
	stageTrace(spec.Temp, "1", "stage two exited %#x", code)
	return code, nil
}

// gitSafeDirectoryEnv appends safe.directory entries for dirs to the Git
// configuration given in the environment: the workspace belongs to the user,
// not to the sandbox account, and Git refuses repositories of other owners.
// The given entries are kept when they are complete; a sequence with gaps
// (the credential filter drops GIT_CONFIG_KEY_n, whose name contains KEY)
// would make Git fail, so it is replaced by the sandbox's entries alone.
func gitSafeDirectoryEnv(env []string, dirs []string) []string {
	if len(dirs) == 0 {
		return env
	}
	count := 0
	present := map[string]bool{}
	for _, kv := range env {
		key, value, _ := strings.Cut(kv, "=")
		key = strings.ToUpper(key)
		if key == "GIT_CONFIG_COUNT" {
			if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && n > 0 {
				count = n
			}
		}
		present[key] = true
	}
	for i := 0; i < count; i++ {
		if !present[fmt.Sprintf("GIT_CONFIG_KEY_%d", i)] || !present[fmt.Sprintf("GIT_CONFIG_VALUE_%d", i)] {
			count = -1
			break
		}
	}
	out := make([]string, 0, len(env)+2*len(dirs)+1)
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		key = strings.ToUpper(key)
		if key == "GIT_CONFIG_COUNT" || count < 0 && (strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_")) {
			continue
		}
		out = append(out, kv)
	}
	count = max(count, 0)
	for _, dir := range dirs {
		for _, value := range []string{filepath.ToSlash(dir), filepath.ToSlash(dir) + "/*"} {
			out = append(out, fmt.Sprintf("GIT_CONFIG_KEY_%d=safe.directory", count), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", count, value))
			count++
		}
	}
	return append(out, "GIT_CONFIG_COUNT="+strconv.Itoa(count))
}

// privateDesktop creates a desktop of its own for the command on the
// interactive window station, so that the sandbox account (which runs at
// the user's integrity level) cannot send input to the user's windows. It
// grants the grantees (the account, and the offline group for the restricted
// token) the desktop and the access to the window station that a process
// needs to start. On other window stations (services, CI) the account inherits the
// launcher's desktop, which the secondary logon service grants it.
func privateDesktop(grantees []string) (string, func(), error) {
	noop := func() {}
	winsta, _, callErr := procGetProcessWindowStation.Call()
	if winsta == 0 {
		return "", noop, fmt.Errorf("window station: %w", callErr)
	}
	station, err := userObjectName(winsta)
	if err != nil {
		return "", noop, err
	}
	if !strings.EqualFold(station, "WinSta0") {
		return "", noop, nil
	}
	for _, sid := range grantees {
		if err := grantWindowStation(station, sid); err != nil {
			return "", noop, fmt.Errorf("let the sandbox account use the window station: %w", err)
		}
	}
	userSID, err := currentUserSID()
	if err != nil {
		return "", noop, err
	}
	sddl := fmt.Sprintf("D:P(A;;GA;;;SY)(A;;GA;;;%s)", userSID)
	for _, sid := range grantees {
		sddl += fmt.Sprintf("(A;;GA;;;%s)", sid)
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return "", noop, err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	name := fmt.Sprintf("KBrainSbx-%d-%d", os.Getpid(), time.Now().UnixNano()%1_000_000_000)
	n, _ := windows.UTF16PtrFromString(name)
	desk, _, callErr := procCreateDesktopW.Call(uintptr(unsafe.Pointer(n)), 0, 0, 0, desktopGenericAll, uintptr(unsafe.Pointer(&sa)))
	if desk == 0 {
		return "", noop, fmt.Errorf("create the sandbox desktop: %w", callErr)
	}
	return station + `\` + name, func() { _, _, _ = procCloseDesktop.Call(desk) }, nil
}

// grantWindowStation adds an entry for accountSID to the window station's
// DACL unless it has one. The window station lives as long as the user's
// logon session, so the entry goes with it.
func grantWindowStation(station, accountSID string) error {
	name, _ := windows.UTF16PtrFromString(station)
	h, _, callErr := procOpenWindowStationW.Call(uintptr(unsafe.Pointer(name)), 0, windows.READ_CONTROL|windows.WRITE_DAC)
	if h == 0 {
		return callErr
	}
	defer procCloseWindowStation.Call(h)
	sd, err := windows.GetSecurityInfo(windows.Handle(h), windows.SE_WINDOW_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	want, err := newACE(accessAllowedACEType, 0, winstaSandboxAccess, accountSID)
	if err != nil {
		return err
	}
	if dacl != nil {
		for i := uint32(0); i < uint32(dacl.AceCount); i++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			if windows.GetAce(dacl, i, &ace) == nil && string(unsafe.Slice((*byte)(unsafe.Pointer(ace)), int(ace.Header.AceSize))) == string(want) {
				return nil
			}
		}
	}
	sid, _ := windows.StringToSid(accountSID)
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{AccessPermissions: winstaSandboxAccess, AccessMode: windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeValue: windows.TrusteeValueFromSID(sid)}}}, dacl)
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(windows.Handle(h), windows.SE_WINDOW_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

// withdrawWindowStation removes the entries grantWindowStation added for sids
// from the launcher's window station; uninstall calls it so that no entry
// for a deleted account stays until the user logs off.
func withdrawWindowStation(sids []string) error {
	winsta, _, callErr := procGetProcessWindowStation.Call()
	if winsta == 0 {
		return fmt.Errorf("window station: %w", callErr)
	}
	station, err := userObjectName(winsta)
	if err != nil {
		return err
	}
	name, _ := windows.UTF16PtrFromString(station)
	h, _, callErr := procOpenWindowStationW.Call(uintptr(unsafe.Pointer(name)), 0, windows.READ_CONTROL|windows.WRITE_DAC)
	if h == 0 {
		return callErr
	}
	defer procCloseWindowStation.Call(h)
	d, err := readObjectDACL(windows.Handle(h), windows.SE_WINDOW_OBJECT)
	if err != nil || d.null {
		return err
	}
	if !d.remove(func(a rawACE) bool { return a.typ() == accessAllowedACEType && slices.Contains(sids, a.sid()) }) {
		return nil
	}
	return writeDACL(windows.Handle(h), d)
}

// runUserStage is stage two, run as the sandbox account.
func runUserStage() int {
	code, err := userStage()
	if err != nil {
		fmt.Fprintf(os.Stderr, "k-brain sandbox: %v\n", err)
		return launcherFailureCode
	}
	return int(code)
}

func userStage() (uint32, error) {
	var spec stageSpec
	if err := decodeJSON(os.Getenv(stageSpecEnv), &spec); err != nil {
		return 0, fmt.Errorf("sandbox stage specification: %w", err)
	}
	if !filepath.IsAbs(spec.Program) || !filepath.IsAbs(spec.Dir) {
		return 0, errors.New("incomplete stage specification")
	}
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		key, _, _ := strings.Cut(kv, "=")
		return strings.EqualFold(key, stageSpecEnv)
	})
	temp := os.Getenv("TEMP")
	stageTrace(temp, "2", "stage two running")
	var self windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_ADJUST_DEFAULT|windows.TOKEN_ADJUST_PRIVILEGES, &self); err != nil {
		return 0, fmt.Errorf("open the sandbox account's token: %w", err)
	}
	defer self.Close()
	user, err := self.GetTokenUser()
	if err != nil {
		return 0, err
	}
	userSID := user.User.Sid.String()
	logon, err := logonSID(self)
	if err != nil {
		return 0, err
	}
	if err := hardenStage(self, userSID); err != nil {
		return 0, err
	}
	var token windows.Token
	if spec.Offline {
		token, err = restrictedToken(self, logon, userSID, spec.Restrict)
	} else {
		token, err = strippedToken(self, logon)
	}
	if err != nil {
		return 0, err
	}
	defer token.Close()
	stdio, err := launcherStdio()
	if err != nil {
		return 0, err
	}
	stageTrace(temp, "2", "token ready; stdio %v; starting %s", stdio, spec.Program)
	defer stageTrace(temp, "2", "command finished")
	return runConfined(confinedProcess{
		program: spec.Program, cmdLine: spec.CmdLine, dir: spec.Dir, env: env,
		stdin: stdio[0], stdout: stdio[1], stderr: stdio[2],
		token: token, inheritDesktop: true, desktop: spec.Desktop, inJob: true,
	})
}

func logonSID(token windows.Token) (string, error) {
	groups, err := token.GetTokenGroups()
	if err != nil {
		return "", err
	}
	for _, g := range groups.AllGroups() {
		if g.Attributes&seGroupLogonID == seGroupLogonID {
			return g.Sid.String(), nil
		}
	}
	return "", errors.New("the sandbox account's token has no logon SID")
}

// hardenStage limits stage two's process, threads and future objects to
// SYSTEM and the account itself. The offline command's restricted token
// lacks the account's SID among its restricting SIDs, so it cannot open stage
// two and borrow its unrestricted token.
func hardenStage(token windows.Token, userSID string) error {
	sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:P(A;;GA;;;SY)(A;;GA;;;%s)", userSID))
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := setDefaultDACL(token, dacl); err != nil {
		return err
	}
	if err := windows.SetSecurityInfo(windows.CurrentProcess(), windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("protect the sandbox stage: %w", err)
	}
	snap, err := windows.CreateToolhelp32Snapshot(th32csSnapThread, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)
	pid := windows.GetCurrentProcessId()
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snap, &entry); err == nil; err = windows.Thread32Next(snap, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		h, err := windows.OpenThread(windows.WRITE_DAC, false, entry.ThreadID)
		if err != nil {
			return fmt.Errorf("protect the sandbox stage: %w", err)
		}
		err = windows.SetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
		windows.CloseHandle(h)
		if err != nil {
			return fmt.Errorf("protect the sandbox stage: %w", err)
		}
	}
	return nil
}

func setDefaultDACL(token windows.Token, dacl *windows.ACL) error {
	info := struct{ dacl *windows.ACL }{dacl}
	if err := windows.SetTokenInformation(token, windows.TokenDefaultDacl, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return fmt.Errorf("set the sandbox token's default access: %w", err)
	}
	return nil
}

// restrictedToken is the offline command's token: the account's token with
// every privilege but bypass-traverse removed and a second access check
// against Everyone, Users, RESTRICTED and extra (the reader SID, one SID per
// writable root, the offline group). The account's own SID is left out, so
// that objects it owns or that name it (anything it created in other
// workspaces) are not opened through it; Authenticated Users is left out, so
// that the directories every signed-in user may create files in stay closed.
// The logon SID is deny-only, as in strippedToken. Its new objects are open
// to SYSTEM and the account (RESTRICTED passing the second check).
func restrictedToken(base windows.Token, logon, userSID string, extra []string) (windows.Token, error) {
	sids := []string{"S-1-1-0", "S-1-5-32-545", "S-1-5-12"}
	for _, s := range extra {
		sids = appendNew(sids, s)
	}
	list := make([]windows.SIDAndAttributes, len(sids))
	for i, s := range sids {
		sid, err := windows.StringToSid(s)
		if err != nil {
			return 0, fmt.Errorf("restricting SID %s: %w", s, err)
		}
		list[i] = windows.SIDAndAttributes{Sid: sid}
	}
	disable, err := logonDenyOnly(logon)
	if err != nil {
		return 0, err
	}
	var token windows.Token
	r, _, callErr := procCreateRestrictedToken.Call(uintptr(base), disableMaxPrivilege, 1, uintptr(unsafe.Pointer(&disable[0])), 0, 0, uintptr(len(list)), uintptr(unsafe.Pointer(&list[0])), uintptr(unsafe.Pointer(&token)))
	if r == 0 {
		return 0, fmt.Errorf("create the offline sandbox token: %w", callErr)
	}
	sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:(A;;GA;;;SY)(A;;GA;;;%s)(A;;GA;;;RC)", userSID))
	if err == nil {
		var dacl *windows.ACL
		if dacl, _, err = sd.DACL(); err == nil {
			err = setDefaultDACL(token, dacl)
		}
	}
	if err != nil {
		token.Close()
		return 0, err
	}
	return token, nil
}

// logonDenyOnly is the SID list that makes the logon SID deny-only. The
// secondary logon service puts the launching user's logon SID into the
// account's token (that is how it reaches the user's desktop); enabled, it
// would open the user's desktop, window station and every object of the
// user's processes that their default DACL grants the logon session.
func logonDenyOnly(logon string) ([]windows.SIDAndAttributes, error) {
	sid, err := windows.StringToSid(logon)
	if err != nil {
		return nil, fmt.Errorf("logon SID %s: %w", logon, err)
	}
	return []windows.SIDAndAttributes{{Sid: sid}}, nil
}

// strippedToken is the online command's token: the account's token without
// privileges and with the logon SID deny-only. It has no restricting SIDs,
// which TLS clients refuse.
func strippedToken(base windows.Token, logon string) (windows.Token, error) {
	disable, err := logonDenyOnly(logon)
	if err != nil {
		return 0, err
	}
	var token windows.Token
	r, _, callErr := procCreateRestrictedToken.Call(uintptr(base), disableMaxPrivilege, 1, uintptr(unsafe.Pointer(&disable[0])), 0, 0, 0, 0, uintptr(unsafe.Pointer(&token)))
	if r == 0 {
		return 0, fmt.Errorf("copy the sandbox account's token: %w", callErr)
	}
	if err := removePrivileges(token); err != nil {
		token.Close()
		return 0, err
	}
	return token, nil
}
