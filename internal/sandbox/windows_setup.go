//go:build windows

package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// The elevated half of the sandbox-user setup: accounts, groups, logon rights,
// the logon-screen entries and the network filters. Setup and Uninstall run it
// in a copy of this binary started through the administrator approval prompt
// (ShellExecuteEx "runas"), or in this process when it is already elevated.
// The approval may come from another administrator than the user, so the
// copy is told whose setup it is (the user's SID) and where the user's state
// lives instead of reading its own profile.

const (
	// elevatedMarker is the first argument of the elevated copy.
	elevatedMarker = "__k-brain-sandbox-elevated__"

	userPrivUser        = 1
	ufScript            = 0x1
	ufPasswdCantChange  = 0x40
	ufNormalAccount     = 0x200
	ufDontExpirePasswd  = 0x10000
	nerrGroupNotFound   = 2220
	nerrUserNotFound    = 2221
	nerrGroupExists     = 2223
	nerrUserExists      = 2224
	errorNoSuchAlias    = 1376
	errorMemberInAlias  = 1378
	errorAliasExists    = 1379
	errorNoSuchMember   = 1387
	statusObjNotFound   = 0xC0000034
	policyCreateAccount = 0x10
	policyLookupNames   = 0x800

	specialAccountsKey = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon\SpecialAccounts\UserList`

	seeMaskNoCloseProcess = 0x40
	seeMaskNoAsync        = 0x100
)

var (
	modnetapi32                 = windows.NewLazySystemDLL("netapi32.dll")
	procNetUserAdd              = modnetapi32.NewProc("NetUserAdd")
	procNetUserDel              = modnetapi32.NewProc("NetUserDel")
	procNetUserSetInfo          = modnetapi32.NewProc("NetUserSetInfo")
	procNetLocalGroupAdd        = modnetapi32.NewProc("NetLocalGroupAdd")
	procNetLocalGroupDel        = modnetapi32.NewProc("NetLocalGroupDel")
	procNetLocalGroupAddMembers = modnetapi32.NewProc("NetLocalGroupAddMembers")
	modadvapi32                 = windows.NewLazySystemDLL("advapi32.dll")
	procLsaOpenPolicy           = modadvapi32.NewProc("LsaOpenPolicy")
	procLsaAddAccountRights     = modadvapi32.NewProc("LsaAddAccountRights")
	procLsaRemoveAccountRights  = modadvapi32.NewProc("LsaRemoveAccountRights")
	procLsaClose                = modadvapi32.NewProc("LsaClose")
	procLsaNtStatusToWinError   = modadvapi32.NewProc("LsaNtStatusToWinError")
	moduserenv                  = windows.NewLazySystemDLL("userenv.dll")
	procDeleteProfileW          = moduserenv.NewProc("DeleteProfileW")
	modshell32                  = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW         = modshell32.NewProc("ShellExecuteExW")
)

// setupAccount is one sandbox account or group in the state file.
type setupAccount struct {
	Name string `json:"name"`
	SID  string `json:"sid"`
	// Password is the account's password encrypted with DPAPI for the
	// machine, with the user's SID as entropy. Groups have none.
	Password string `json:"password,omitempty"`
}

// setupState is the sandbox-user state of one user.
type setupState struct {
	Version      int          `json:"version"`
	UserSID      string       `json:"user_sid"`
	Online       setupAccount `json:"online"`
	Offline      setupAccount `json:"offline"`
	Group        setupAccount `json:"group"`
	OfflineGroup setupAccount `json:"offline_group"`
	Created      time.Time    `json:"created"`

	path  string
	names setupNames
}

func currentUserSID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read the current user: %w", err)
	}
	return user.User.Sid.String(), nil
}

func setupStatePath() (string, error) {
	dir := setupStateDir()
	if dir == "" {
		return "", errors.New("the K-brain data directory is unknown (no home directory)")
	}
	return filepath.Join(dir, setupStateFile), nil
}

// loadSetupState reads the current user's state. It returns fs.ErrNotExist
// when the setup was never run.
func loadSetupState() (*setupState, error) {
	path, err := setupStatePath()
	if err != nil {
		return nil, err
	}
	return loadSetupStateAt(path)
}

func loadSetupStateAt(path string) (*setupState, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st setupState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if !strings.EqualFold(st.UserSID, sid) {
		return nil, fmt.Errorf("%s belongs to another user", path)
	}
	st.path, st.names = path, namesFor(sid)
	for _, a := range []setupAccount{st.Online, st.Offline, st.Group, st.OfflineGroup} {
		if a.Name == "" || !strings.HasPrefix(a.SID, "S-1-5-") {
			return nil, fmt.Errorf("%s is incomplete", path)
		}
	}
	return &st, nil
}

func (st *setupState) password(a setupAccount) (string, error) {
	blob, err := base64.StdEncoding.DecodeString(a.Password)
	if err != nil || len(blob) == 0 {
		return "", fmt.Errorf("the stored password of %s is unreadable", a.Name)
	}
	plain, err := dpapi(blob, st.UserSID, false)
	if err != nil {
		return "", fmt.Errorf("decrypt the password of %s: %w", a.Name, err)
	}
	return string(plain), nil
}

func dpapi(data []byte, entropy string, protect bool) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	ent := []byte(strings.ToUpper(entropy))
	entBlob := windows.DataBlob{Size: uint32(len(ent)), Data: &ent[0]}
	var out windows.DataBlob
	const flags = windows.CRYPTPROTECT_UI_FORBIDDEN | windows.CRYPTPROTECT_LOCAL_MACHINE
	var err error
	if protect {
		err = windows.CryptProtectData(&in, nil, &entBlob, 0, nil, flags, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, &entBlob, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

// elevatedRequest is what the elevated copy is asked to do.
type elevatedRequest struct {
	Op       string `json:"op"`
	UserSID  string `json:"user_sid"`
	StateDir string `json:"state_dir"`
	Result   string `json:"result"`
	// Namespace is the test namespace (setupNamespace).
	Namespace string `json:"namespace,omitempty"`
}

type elevatedResult struct {
	Error string `json:"error,omitempty"`
}

func init() {
	if len(os.Args) == 3 && os.Args[1] == elevatedMarker {
		os.Exit(runElevatedCopy(os.Args[2]))
	}
}

func runElevatedCopy(encoded string) int {
	var req elevatedRequest
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err == nil {
		err = json.Unmarshal(data, &req)
	}
	if err != nil || req.Result == "" || !filepath.IsAbs(req.Result) {
		fmt.Fprintln(os.Stderr, "k-brain sandbox: invalid elevated request")
		return 2
	}
	setupNamespace = req.Namespace
	err = runElevated(req)
	res := elevatedResult{}
	if err != nil {
		res.Error = err.Error()
	}
	out, _ := json.Marshal(res)
	if werr := os.WriteFile(req.Result, out, 0o600); werr != nil {
		fmt.Fprintln(os.Stderr, "k-brain sandbox:", werr)
		return 1
	}
	if err != nil {
		return 1
	}
	return 0
}

func runElevated(req elevatedRequest) error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("the sandbox setup needs administrator rights")
	}
	if _, err := windows.StringToSid(req.UserSID); err != nil || !strings.HasPrefix(req.UserSID, "S-1-5-21-") {
		return fmt.Errorf("invalid user SID %q", req.UserSID)
	}
	if !filepath.IsAbs(req.StateDir) {
		return fmt.Errorf("invalid state directory %q", req.StateDir)
	}
	switch req.Op {
	case "setup":
		return elevatedSetup(req.UserSID, req.StateDir)
	case "uninstall":
		return elevatedUninstall(req.UserSID)
	default:
		return fmt.Errorf("unknown operation %q", req.Op)
	}
}

// runAsAdministrator runs req in this process when it is elevated, and
// otherwise in a copy of this binary started through the administrator
// approval prompt. A declined prompt is ErrSetupDeclined.
func runAsAdministrator(ctx context.Context, req elevatedRequest) error {
	if windows.GetCurrentProcessToken().IsElevated() {
		return runElevated(req)
	}
	dir, err := os.MkdirTemp("", "k-brain-sandbox-setup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	req.Result = filepath.Join(dir, "result.json")
	req.Namespace = setupNamespace
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	process, err := shellExecuteRunAs(self, elevatedMarker+" "+base64.RawURLEncoding.EncodeToString(data))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	for {
		ev, err := windows.WaitForSingleObject(process, 500)
		if err != nil {
			return err
		}
		if ev == windows.WAIT_OBJECT_0 {
			break
		}
		if ctx.Err() != nil {
			// The elevated copy finishes on its own; the caller stops waiting.
			return ctx.Err()
		}
	}
	out, err := os.ReadFile(req.Result)
	if err != nil {
		var code uint32
		_ = windows.GetExitCodeProcess(process, &code)
		return fmt.Errorf("the elevated sandbox %s exited with code %d without a result", req.Op, code)
	}
	var res elevatedResult
	if err := json.Unmarshal(out, &res); err != nil {
		return err
	}
	if res.Error != "" {
		return errors.New(res.Error)
	}
	return nil
}

type shellExecuteInfo struct {
	cbSize      uint32
	fMask       uint32
	hwnd        uintptr
	verb        *uint16
	file        *uint16
	parameters  *uint16
	directory   *uint16
	show        int32
	instApp     uintptr
	idList      uintptr
	class       *uint16
	keyClass    uintptr
	hotKey      uint32
	iconMonitor uintptr
	process     windows.Handle
}

// shellExecuteRunAs starts program elevated, showing the administrator
// approval prompt, and returns its process handle.
func shellExecuteRunAs(program, params string) (windows.Handle, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err == nil {
		defer windows.CoUninitialize()
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(program)
	if err != nil {
		return 0, err
	}
	args, err := windows.UTF16PtrFromString(params)
	if err != nil {
		return 0, err
	}
	info := shellExecuteInfo{fMask: seeMaskNoCloseProcess | seeMaskNoAsync, verb: verb, file: file, parameters: args, show: windows.SW_HIDE}
	info.cbSize = uint32(unsafe.Sizeof(info))
	r, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return 0, ErrSetupDeclined
		}
		return 0, fmt.Errorf("ask for administrator approval: %w", callErr)
	}
	if info.process == 0 {
		return 0, errors.New("ask for administrator approval: no process was started")
	}
	return info.process, nil
}

// setupPlatform creates or repairs the current user's setup.
func setupPlatform(ctx context.Context) error {
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	path, err := setupStatePath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	err = runAsAdministrator(ctx, elevatedRequest{Op: "setup", UserSID: sid, StateDir: dir})
	forgetSandboxState()
	return err
}

// uninstallPlatform removes the current user's setup: first the access
// entries (as the user, who owns the paths), then the elevated parts, then
// the state. Every step skips what is already gone.
func uninstallPlatform(ctx context.Context) error {
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	path, err := setupStatePath()
	if err != nil {
		return err
	}
	n := namesFor(sid)
	var groups []string
	if st, err := loadSetupState(); err == nil {
		groups = []string{st.Group.SID, st.OfflineGroup.SID}
	} else {
		for _, name := range []string{n.group, n.offlineGroup} {
			if s, _, _, err := windows.LookupSID("", name); err == nil {
				groups = append(groups, s.String())
			}
		}
	}
	var errs []error
	rootsFile := filepath.Join(filepath.Dir(path), aclRootsFile)
	roots, err := readACLRoots(rootsFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	ours := n.sandboxACE(groups)
	for _, root := range roots {
		if err := removeTree(root, ours); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("remove the sandbox access entries: %w", errors.Join(errs...))
	}
	if err := runAsAdministrator(ctx, elevatedRequest{Op: "uninstall", UserSID: sid, StateDir: filepath.Dir(path)}); err != nil {
		return err
	}
	for _, f := range []string{path, rootsFile} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	aclRecordMu.Lock()
	clear(aclRecorded)
	aclRecordMu.Unlock()
	forgetSandboxState()
	return nil
}

// statusPlatform fills in what the state file says.
func statusPlatform(s *SetupStatus) {
	path, err := setupStatePath()
	if err == nil {
		s.StatePath = path
	}
	st, err := loadSetupState()
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.Problem = err.Error()
		}
		return
	}
	s.Installed = true
	s.Accounts = []string{st.Online.Name, st.Offline.Name}
	s.Groups = []string{st.Group.Name, st.OfflineGroup.Name}
	if !s.Active {
		if p := lastSandboxUserProblem(); p != "" {
			s.Problem = p
		}
	}
}

// elevatedSetup creates or repairs the accounts, groups, rights, logon-screen
// entries and filters of userSID's setup and writes the state file.
func elevatedSetup(userSID, stateDir string) error {
	n := namesFor(userSID)
	st := setupState{Version: 1, UserSID: userSID, Created: time.Now().UTC()}
	var err error
	if st.Group, err = ensureLocalGroup(n.group, "K-brain sandbox accounts"); err != nil {
		return err
	}
	if st.OfflineGroup, err = ensureLocalGroup(n.offlineGroup, "K-brain offline sandbox account (network blocked)"); err != nil {
		return err
	}
	if st.Online, err = ensureSandboxUser(n.online, "K-brain sandbox account (network allowed)", userSID); err != nil {
		return err
	}
	if st.Offline, err = ensureSandboxUser(n.offline, "K-brain sandbox account (network blocked)", userSID); err != nil {
		return err
	}
	for _, m := range []struct{ group, sid string }{{n.group, st.Online.SID}, {n.group, st.Offline.SID}, {n.offlineGroup, st.Offline.SID}} {
		if err := addGroupMember(m.group, m.sid); err != nil {
			return err
		}
	}
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, specialAccountsKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("hide the sandbox accounts from the logon screen: %w", err)
	}
	for _, name := range []string{n.online, n.offline} {
		if err := key.SetDWordValue(name, 0); err != nil {
			key.Close()
			return fmt.Errorf("hide the sandbox accounts from the logon screen: %w", err)
		}
	}
	key.Close()
	for _, sid := range []string{st.Online.SID, st.Offline.SID} {
		// Local (interactive) logon is how the sandbox starts the accounts;
		// network and remote desktop logons are denied.
		if err := lsaAccountRights(sid, true, "SeInteractiveLogonRight", "SeDenyNetworkLogonRight", "SeDenyRemoteInteractiveLogonRight"); err != nil {
			return err
		}
	}
	if err := installWFP(n, st.OfflineGroup.SID); err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(stateDir, setupStateFile)
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	f, err := openPrivateFile(tmp, userSID, st.Group.SID, windows.GENERIC_WRITE, windows.CREATE_ALWAYS)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", path, err)
	}
	// The user owns the state, so that the user's own uninstall can remove it.
	if owner, err := windows.StringToSid(userSID); err == nil {
		_ = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION, owner, nil, nil, nil)
	}
	return nil
}

// elevatedUninstall removes the filters, rights, profiles, accounts,
// logon-screen entries and groups of userSID's setup. What does not exist is
// skipped, so it can run any number of times.
func elevatedUninstall(userSID string) error {
	n := namesFor(userSID)
	var errs []error
	if err := removeWFP(n); err != nil {
		errs = append(errs, err)
	}
	for _, name := range []string{n.online, n.offline} {
		sid, _, _, err := windows.LookupSID("", name)
		if err == nil {
			s := sid.String()
			if err := lsaAccountRights(s, false); err != nil {
				errs = append(errs, err)
			}
			if p, _ := windows.UTF16PtrFromString(s); p != nil {
				_, _, _ = procDeleteProfileW.Call(uintptr(unsafe.Pointer(p)), 0, 0)
			}
		}
		if err := netCall(procNetUserDel, name); err != nil && !netIs(err, nerrUserNotFound) {
			errs = append(errs, fmt.Errorf("delete account %s: %w", name, err))
		}
	}
	if key, err := registry.OpenKey(registry.LOCAL_MACHINE, specialAccountsKey, registry.SET_VALUE); err == nil {
		for _, name := range []string{n.online, n.offline} {
			if err := key.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
				errs = append(errs, err)
			}
		}
		key.Close()
	}
	for _, name := range []string{n.offlineGroup, n.group} {
		if err := netCall(procNetLocalGroupDel, name); err != nil && !netIs(err, nerrGroupNotFound, errorNoSuchAlias) {
			errs = append(errs, fmt.Errorf("delete group %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func netIs(err error, codes ...uint32) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	for _, c := range codes {
		if uint32(errno) == c {
			return true
		}
	}
	return false
}

// netCall calls a netapi32 function whose arguments are a NULL server and
// the given strings.
func netCall(p *windows.LazyProc, names ...string) error {
	args := []uintptr{0}
	for _, s := range names {
		ptr, err := windows.UTF16PtrFromString(s)
		if err != nil {
			return err
		}
		args = append(args, uintptr(unsafe.Pointer(ptr)))
	}
	r, _, _ := p.Call(args...)
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

func ensureLocalGroup(name, comment string) (setupAccount, error) {
	type localGroupInfo1 struct{ name, comment *uint16 }
	n, _ := windows.UTF16PtrFromString(name)
	c, _ := windows.UTF16PtrFromString(comment)
	info := localGroupInfo1{n, c}
	r, _, _ := procNetLocalGroupAdd.Call(0, 1, uintptr(unsafe.Pointer(&info)), 0)
	if r != 0 && r != nerrGroupExists && r != errorAliasExists {
		return setupAccount{}, fmt.Errorf("create group %s: %w", name, syscall.Errno(r))
	}
	return lookupAccount(name)
}

func lookupAccount(name string) (setupAccount, error) {
	sid, _, _, err := windows.LookupSID("", name)
	if err != nil {
		return setupAccount{}, fmt.Errorf("look up %s: %w", name, err)
	}
	return setupAccount{Name: name, SID: sid.String()}, nil
}

// ensureSandboxUser creates the account, or resets the password and flags of
// an existing one, and returns it with its new password encrypted.
func ensureSandboxUser(name, comment, userSID string) (setupAccount, error) {
	password, err := randomPassword()
	if err != nil {
		return setupAccount{}, err
	}
	type userInfo1 struct {
		name, password *uint16
		passwordAge    uint32
		priv           uint32
		homeDir        *uint16
		comment        *uint16
		flags          uint32
		scriptPath     *uint16
	}
	n, _ := windows.UTF16PtrFromString(name)
	pw, _ := windows.UTF16PtrFromString(password)
	c, _ := windows.UTF16PtrFromString(comment)
	flags := uint32(ufScript | ufNormalAccount | ufDontExpirePasswd | ufPasswdCantChange)
	info := userInfo1{name: n, password: pw, priv: userPrivUser, comment: c, flags: flags}
	r, _, _ := procNetUserAdd.Call(0, 1, uintptr(unsafe.Pointer(&info)), 0)
	switch r {
	case 0:
	case nerrUserExists:
		set := struct{ password *uint16 }{pw}
		if r, _, _ := procNetUserSetInfo.Call(0, uintptr(unsafe.Pointer(n)), 1003, uintptr(unsafe.Pointer(&set)), 0); r != 0 {
			return setupAccount{}, fmt.Errorf("reset the password of %s: %w", name, syscall.Errno(r))
		}
		setFlags := struct{ flags uint32 }{flags}
		if r, _, _ := procNetUserSetInfo.Call(0, uintptr(unsafe.Pointer(n)), 1008, uintptr(unsafe.Pointer(&setFlags)), 0); r != 0 {
			return setupAccount{}, fmt.Errorf("enable %s: %w", name, syscall.Errno(r))
		}
	default:
		return setupAccount{}, fmt.Errorf("create account %s: %w", name, syscall.Errno(r))
	}
	a, err := lookupAccount(name)
	if err != nil {
		return a, err
	}
	blob, err := dpapi([]byte(password), userSID, true)
	if err != nil {
		return a, fmt.Errorf("encrypt the password of %s: %w", name, err)
	}
	a.Password = base64.StdEncoding.EncodeToString(blob)
	return a, nil
}

func addGroupMember(group, sid string) error {
	s, err := windows.StringToSid(sid)
	if err != nil {
		return err
	}
	g, _ := windows.UTF16PtrFromString(group)
	member := struct{ sid *windows.SID }{s}
	r, _, _ := procNetLocalGroupAddMembers.Call(0, uintptr(unsafe.Pointer(g)), 0, uintptr(unsafe.Pointer(&member)), 1)
	if r != 0 && r != errorMemberInAlias {
		return fmt.Errorf("add %s to %s: %w", sid, group, syscall.Errno(r))
	}
	return nil
}

// lsaAccountRights adds the named rights to sid, or with add false removes
// all of its rights.
func lsaAccountRights(sid string, add bool, rights ...string) error {
	s, err := windows.StringToSid(sid)
	if err != nil {
		return err
	}
	attrs := windows.OBJECT_ATTRIBUTES{Length: uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{}))}
	var policy uintptr
	if st, _, _ := procLsaOpenPolicy.Call(0, uintptr(unsafe.Pointer(&attrs)), policyCreateAccount|policyLookupNames, uintptr(unsafe.Pointer(&policy))); st != 0 {
		return fmt.Errorf("open the local security policy: %w", lsaError(st))
	}
	defer procLsaClose.Call(policy)
	if !add {
		st, _, _ := procLsaRemoveAccountRights.Call(policy, uintptr(unsafe.Pointer(s)), 1, 0, 0)
		if st != 0 && st != statusObjNotFound {
			return fmt.Errorf("remove the logon rights of %s: %w", sid, lsaError(st))
		}
		return nil
	}
	list := make([]windows.NTUnicodeString, len(rights))
	for i, r := range rights {
		p, _ := windows.UTF16FromString(r)
		list[i] = windows.NTUnicodeString{Length: uint16((len(p) - 1) * 2), MaximumLength: uint16(len(p) * 2), Buffer: &p[0]}
	}
	if st, _, _ := procLsaAddAccountRights.Call(policy, uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(&list[0])), uintptr(len(list))); st != 0 {
		return fmt.Errorf("set the logon rights of %s: %w", sid, lsaError(st))
	}
	return nil
}

func lsaError(status uintptr) error {
	r, _, _ := procLsaNtStatusToWinError.Call(status)
	return syscall.Errno(r)
}

// randomPassword returns a 32-character password with every character class,
// which meets any local password policy.
func randomPassword() (string, error) {
	classes := []string{"ABCDEFGHJKLMNPQRSTUVWXYZ", "abcdefghijkmnopqrstuvwxyz", "23456789", "!#%+-.:=?@^_~"}
	all := strings.Join(classes, "")
	pick := func(set string) (byte, error) {
		i, err := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
		if err != nil {
			return 0, err
		}
		return set[i.Int64()], nil
	}
	out := make([]byte, 0, 32)
	for _, set := range classes {
		c, err := pick(set)
		if err != nil {
			return "", err
		}
		out = append(out, c)
	}
	for len(out) < 32 {
		c, err := pick(all)
		if err != nil {
			return "", err
		}
		out = append(out, c)
	}
	return string(out), nil
}

// sandboxState caches the loaded state for the run path.
var (
	sandboxStateMu sync.Mutex
	sandboxStateV  *setupState
)

func cachedSandboxState() (*setupState, error) {
	sandboxStateMu.Lock()
	defer sandboxStateMu.Unlock()
	if sandboxStateV != nil {
		return sandboxStateV, nil
	}
	st, err := loadSetupState()
	if err != nil {
		return nil, err
	}
	sandboxStateV = st
	return st, nil
}

func forgetSandboxState() {
	sandboxStateMu.Lock()
	sandboxStateV = nil
	sandboxStateMu.Unlock()
}
