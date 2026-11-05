//go:build windows

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// launchMarker is the first argument of the launcher re-exec. Every binary that
// links this package (k-brain, k-brain-computer, k-brain-vet and test binaries)
// handles it in init, before main, so Wrap only depends on os.Executable.
//
// The launcher exists because os/exec cannot start a process the way the
// sandbox needs: SysProcAttr.Token alone gives no job object from the first
// instruction, no handle allow-list and no protected paths held open. The launcher calls
// CreateProcessAsUser itself, waits for the command and exits with its code.
// It never runs the command unconfined: any failure exits with
// launcherFailureCode and a message on stderr.
const (
	launchMarker        = "__k-brain-sandbox-launch__"
	launcherFailureCode = 125
)

func init() {
	if len(os.Args) == 3 && os.Args[1] == launchMarker {
		os.Exit(runLauncher(os.Args[2]))
	}
}

func runLauncher(encoded string) int {
	spec, err := decodeLaunchSpec(encoded)
	if err == nil {
		var stdio [3]windows.Handle
		stdio, err = launcherStdio()
		if err == nil {
			var code uint32
			code, err = runConfined(confinedProcess{
				program: spec.Program, cmdLine: spec.CmdLine, dir: spec.Dir, pins: spec.Pins,
				env:   confinedEnv(os.Environ(), spec.Temp),
				stdin: stdio[0], stdout: stdio[1], stderr: stdio[2],
			})
			if err == nil {
				return int(code)
			}
		}
	}
	fmt.Fprintf(os.Stderr, "k-brain sandbox: %v\n", err)
	return launcherFailureCode
}

// confinedEnv points the temporary directory variables at the fenced temp and
// stops cmd.exe and CreateProcess from searching the working directory first.
func confinedEnv(env []string, temp string) []string {
	out := make([]string, 0, len(env)+4)
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(key) {
		case "TEMP", "TMP", "TMPDIR", "NODEFAULTCURRENTDIRECTORYINEXEPATH":
			continue
		}
		out = append(out, kv)
	}
	if temp != "" {
		out = append(out, "TEMP="+temp, "TMP="+temp, "TMPDIR="+temp)
	}
	return append(out, "NoDefaultCurrentDirectoryInExePath=1")
}

func launcherStdio() ([3]windows.Handle, error) {
	var out [3]windows.Handle
	for i, which := range []uint32{windows.STD_INPUT_HANDLE, windows.STD_OUTPUT_HANDLE, windows.STD_ERROR_HANDLE} {
		h, err := windows.GetStdHandle(which)
		if err != nil || h == 0 || h == windows.InvalidHandle {
			if h, err = openNull(); err != nil {
				return out, err
			}
		}
		out[i] = h
	}
	return out, nil
}

func openNull() (windows.Handle, error) {
	name, _ := windows.UTF16PtrFromString("NUL")
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	return windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, &sa, windows.OPEN_EXISTING, 0, 0)
}

type confinedProcess struct {
	program, cmdLine, dir string
	env                   []string
	// pins are held open without delete sharing while the command runs.
	pins                  []string
	stdin, stdout, stderr windows.Handle
}

const (
	procThreadAttributeJobList = 0x0002000D
	uoiName                    = 2
)

var (
	moduser32                     = windows.NewLazySystemDLL("user32.dll")
	procGetProcessWindowStation   = moduser32.NewProc("GetProcessWindowStation")
	procGetUserObjectInformationW = moduser32.NewProc("GetUserObjectInformationW")
)

// runConfined pins the protected paths, starts the command at low integrity
// inside a kill-on-close job assigned at creation (so not even the first
// instruction runs outside it), with only the three stdio handles inherited,
// and returns its exit code. The job is closed on return, which kills whatever
// the command left running, as a process namespace would, so nothing confined
// outlives the pins.
func runConfined(p confinedProcess) (uint32, error) {
	pins, err := pinPaths(p.pins)
	if err != nil {
		return 0, err
	}
	defer func() {
		for _, h := range pins {
			_ = windows.CloseHandle(h)
		}
	}()
	token, err := lowIntegrityToken()
	if err != nil {
		return 0, err
	}
	defer token.Close()
	desktop, err := sandboxDesktop()
	if err != nil {
		return 0, err
	}
	job, err := confinementJob()
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(job)

	handles := make([]windows.Handle, 0, 3)
	for _, h := range []windows.Handle{p.stdin, p.stdout, p.stderr} {
		if err := windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			return 0, fmt.Errorf("prepare stdio: %w", err)
		}
		if !slices.Contains(handles, h) {
			handles = append(handles, h)
		}
	}
	attrs, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return 0, err
	}
	defer attrs.Delete()
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
		return 0, fmt.Errorf("handle list: %w", err)
	}
	jobs := []windows.Handle{job}
	if err := attrs.Update(procThreadAttributeJobList, unsafe.Pointer(&jobs[0]), unsafe.Sizeof(jobs[0])); err != nil {
		return 0, fmt.Errorf("job list: %w", err)
	}

	var desktopName *uint16
	if desktop != "" {
		if desktopName, err = windows.UTF16PtrFromString(desktop); err != nil {
			return 0, err
		}
	}
	si := &windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb:        uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			Desktop:   desktopName,
			Flags:     windows.STARTF_USESTDHANDLES,
			StdInput:  p.stdin,
			StdOutput: p.stdout,
			StdErr:    p.stderr,
		},
		ProcThreadAttributeList: attrs.List(),
	}
	app, err := windows.UTF16PtrFromString(p.program)
	if err != nil {
		return 0, err
	}
	cmdLine, err := windows.UTF16FromString(p.cmdLine)
	if err != nil {
		return 0, err
	}
	dir, err := windows.UTF16PtrFromString(p.dir)
	if err != nil {
		return 0, err
	}
	env, err := environmentBlock(p.env)
	if err != nil {
		return 0, err
	}
	var pi windows.ProcessInformation
	flags := uint32(windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_NO_WINDOW)
	if err := windows.CreateProcessAsUser(token, app, &cmdLine[0], nil, nil, true, flags, &env[0], dir, &si.StartupInfo, &pi); err != nil {
		return 0, fmt.Errorf("start %s at low integrity: %w", p.program, err)
	}
	defer windows.CloseHandle(pi.Process)
	_ = windows.CloseHandle(pi.Thread)
	if _, err := windows.WaitForSingleObject(pi.Process, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil {
		return 0, err
	}
	return code, nil
}

// pinPaths opens each path for reading without FILE_SHARE_DELETE. While the
// handles are open, opening the path for deletion (which renaming needs too)
// fails with a sharing violation. The handles need data access: the file
// system ignores the share mode of a handle opened only for attributes. A path that no longer exists is skipped: it was removed
// before the command started, so the command can only recreate it, as with a
// protected path that never existed.
func pinPaths(paths []string) ([]windows.Handle, error) {
	var handles []windows.Handle
	for _, path := range paths {
		name, err := windows.UTF16PtrFromString(path)
		if err == nil {
			var h windows.Handle
			h, err = windows.CreateFile(name, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
				windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
			if err == nil {
				handles = append(handles, h)
				continue
			}
		}
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			continue
		}
		for _, h := range handles {
			_ = windows.CloseHandle(h)
		}
		return nil, fmt.Errorf("hold protected path %s: %w", path, err)
	}
	return handles, nil
}

// lowIntegrityToken derives the command's token from this process's token: a
// copy with every privilege but SeChangeNotifyPrivilege removed and integrity
// lowered to low. It is deliberately not a restricted token (no restricting
// SIDs, no filtered groups): the system's TLS provider refuses credentials to
// a token with restricting SIDs, and a filtered token cannot start a console
// program without a console window.
func lowIntegrityToken() (windows.Token, error) {
	var current windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_ADJUST_DEFAULT, &current); err != nil {
		return 0, fmt.Errorf("open process token: %w", err)
	}
	defer current.Close()
	var token windows.Token
	if err := windows.DuplicateTokenEx(current, windows.MAXIMUM_ALLOWED, nil, windows.SecurityImpersonation, windows.TokenPrimary, &token); err != nil {
		return 0, fmt.Errorf("copy the process token: %w", err)
	}
	if err := removePrivileges(token); err != nil {
		token.Close()
		return 0, err
	}
	low, err := windows.CreateWellKnownSid(windows.WinLowLabelSid)
	if err != nil {
		token.Close()
		return 0, err
	}
	label := windows.Tokenmandatorylabel{Label: windows.SIDAndAttributes{Sid: low, Attributes: windows.SE_GROUP_INTEGRITY}}
	if err := windows.SetTokenInformation(token, windows.TokenIntegrityLevel, (*byte)(unsafe.Pointer(&label)), uint32(unsafe.Sizeof(label))+windows.GetLengthSid(low)); err != nil {
		token.Close()
		return 0, fmt.Errorf("lower the sandbox token to low integrity: %w", err)
	}
	return token, nil
}

// removePrivileges removes every privilege from token except
// SeChangeNotifyPrivilege (bypass traverse checking, which ordinary path
// lookups rely on).
func removePrivileges(token windows.Token) error {
	var n uint32
	_ = windows.GetTokenInformation(token, windows.TokenPrivileges, nil, 0, &n)
	if n == 0 {
		return errors.New("read the sandbox token privileges")
	}
	buf := make([]byte, n)
	if err := windows.GetTokenInformation(token, windows.TokenPrivileges, &buf[0], n, &n); err != nil {
		return fmt.Errorf("read the sandbox token privileges: %w", err)
	}
	privs := (*windows.Tokenprivileges)(unsafe.Pointer(&buf[0]))
	var keep windows.LUID
	name, _ := windows.UTF16PtrFromString("SeChangeNotifyPrivilege")
	if err := windows.LookupPrivilegeValue(nil, name, &keep); err != nil {
		return err
	}
	all := privs.AllPrivileges()
	removed := 0
	for i := range all {
		if all[i].Luid == keep {
			continue
		}
		all[removed] = windows.LUIDAndAttributes{Luid: all[i].Luid, Attributes: windows.SE_PRIVILEGE_REMOVED}
		removed++
	}
	if removed == 0 {
		return nil
	}
	privs.PrivilegeCount = uint32(removed)
	if err := windows.AdjustTokenPrivileges(token, false, privs, 0, nil, nil); err != nil {
		return fmt.Errorf("remove the sandbox token privileges: %w", err)
	}
	return nil
}

// sandboxDesktop is the desktop to name in the startup information. On the
// interactive window station the command is given WinSta0\Default explicitly,
// which a low-integrity process can open. Elsewhere (a service's window
// station, as in CI) it inherits the launcher's desktop, which it could not
// open by name. Either way user interface privilege isolation keeps it from
// sending messages to, or hooking, the user's higher-integrity windows.
func sandboxDesktop() (string, error) {
	winsta, _, callErr := procGetProcessWindowStation.Call()
	if winsta == 0 {
		return "", fmt.Errorf("window station: %w", callErr)
	}
	name, err := userObjectName(winsta)
	if err != nil {
		return "", err
	}
	if strings.EqualFold(name, "WinSta0") {
		return `WinSta0\Default`, nil
	}
	return "", nil
}

func userObjectName(h uintptr) (string, error) {
	buf := make([]uint16, 256)
	var needed uint32
	r, _, callErr := procGetUserObjectInformationW.Call(h, uoiName, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2), uintptr(unsafe.Pointer(&needed)))
	if r == 0 {
		return "", fmt.Errorf("user object name: %w", callErr)
	}
	return windows.UTF16ToString(buf), nil
}

// confinementJob kills every process in it when its last handle closes (the
// launcher exiting or being killed), forbids breaking away, and blocks the
// user-interface operations that reach outside the command (clipboard,
// global atoms, other processes' user handles, system settings).
func confinementJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create job: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return 0, fmt.Errorf("configure job: %w", err)
	}
	ui := windows.JOBOBJECT_BASIC_UI_RESTRICTIONS{UIRestrictionsClass: windows.JOB_OBJECT_UILIMIT_DESKTOP | windows.JOB_OBJECT_UILIMIT_DISPLAYSETTINGS |
		windows.JOB_OBJECT_UILIMIT_EXITWINDOWS | windows.JOB_OBJECT_UILIMIT_GLOBALATOMS | windows.JOB_OBJECT_UILIMIT_HANDLES |
		windows.JOB_OBJECT_UILIMIT_READCLIPBOARD | windows.JOB_OBJECT_UILIMIT_SYSTEMPARAMETERS | windows.JOB_OBJECT_UILIMIT_WRITECLIPBOARD}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectBasicUIRestrictions, uintptr(unsafe.Pointer(&ui)), uint32(unsafe.Sizeof(ui))); err != nil {
		windows.CloseHandle(job)
		return 0, fmt.Errorf("configure job: %w", err)
	}
	return job, nil
}

// environmentBlock builds a sorted, de-duplicated (last value wins, keys
// compared case-insensitively) Unicode environment block.
func environmentBlock(env []string) ([]uint16, error) {
	values := map[string]string{}
	var keys []string
	for _, kv := range env {
		key, _, ok := strings.Cut(kv[min(1, len(kv)):], "=")
		if !ok {
			continue
		}
		key = kv[:min(1, len(kv))] + key
		upper := strings.ToUpper(key)
		if _, seen := values[upper]; !seen {
			keys = append(keys, upper)
		}
		values[upper] = kv
	}
	if _, ok := values["SYSTEMROOT"]; !ok {
		keys = append(keys, "SYSTEMROOT")
		values["SYSTEMROOT"] = "SYSTEMROOT=" + systemRoot()
	}
	slices.Sort(keys)
	var block []uint16
	for _, key := range keys {
		kv := values[key]
		if strings.IndexByte(kv, 0) >= 0 {
			return nil, fmt.Errorf("environment variable %q contains NUL", key)
		}
		block = append(block, windows.StringToUTF16(kv)...)
	}
	if len(block) == 0 {
		block = append(block, 0)
	}
	return append(block, 0), nil
}
