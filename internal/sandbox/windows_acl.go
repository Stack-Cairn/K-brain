//go:build windows

package sandbox

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Access entries of the sandbox-user level. The sandbox accounts are ordinary
// standard users, so what they may touch is decided by the discretionary ACLs:
// the user's files grant them nothing, and the sandbox adds entries for its
// group (and, for the restricted token of offline runs, for synthetic SIDs) on
// the run's paths. Like the integrity labels of the low-integrity level the
// entries are propagated by walking the tree here rather than by
// SetNamedSecurityInfo, so that propagation never follows a junction or
// symbolic link out of the tree, and they stay after the run for the next one.
// Every path that gets an entry is recorded (aclRootsFile) so that uninstall
// can take the entries away again.

const (
	accessAllowedACEType = 0
	accessDeniedACEType  = 1

	aceInheritOnly = 0x8
	aceInherited   = 0x10

	// fileModify is the Modify right without DELETE: the top of a writable
	// root may be written but not deleted or renamed.
	fileModify       = 0x1301BF
	fileModifyNoDel  = fileModify &^ windows.DELETE
	fileReadExecute  = 0x1200A9
	fileAll          = 0x1F01FF
	fileWriteRights  = 0x2 | 0x4 | 0x10 | 0x40 | 0x100 | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER
	seDACLProtected  = 0x1000
	seDACLAutoInheri = 0x0400
)

// aceSpec is one access entry the sandbox wants on a path.
type aceSpec struct {
	typ  byte
	mask uint32
	sid  string
}

// rawACE is one entry of a DACL, kept as its bytes so that entries of types
// the sandbox does not know survive a rewrite unchanged.
type rawACE []byte

func (a rawACE) typ() byte    { return a[0] }
func (a rawACE) flags() byte  { return a[1] }
func (a rawACE) mask() uint32 { return binary.LittleEndian.Uint32(a[4:8]) }

// sid returns the SID of an allow or deny entry, or "" for other types.
func (a rawACE) sid() string {
	if (a.typ() != accessAllowedACEType && a.typ() != accessDeniedACEType) || len(a) < 16 {
		return ""
	}
	return (*windows.SID)(unsafe.Pointer(&a[8])).String()
}

func newACE(typ, flags byte, mask uint32, sid string) (rawACE, error) {
	s, err := windows.StringToSid(sid)
	if err != nil {
		return nil, fmt.Errorf("sandbox SID %s: %w", sid, err)
	}
	n := int(windows.GetLengthSid(s))
	ace := make(rawACE, 8+n)
	ace[0], ace[1] = typ, flags
	binary.LittleEndian.PutUint16(ace[2:4], uint16(8+n))
	binary.LittleEndian.PutUint32(ace[4:8], mask)
	copy(ace[8:], unsafe.Slice((*byte)(unsafe.Pointer(s)), n))
	return ace, nil
}

// fileDACL is the DACL of one open file or directory.
type fileDACL struct {
	aces    []rawACE
	control windows.SECURITY_DESCRIPTOR_CONTROL
	null    bool
}

func readDACL(h windows.Handle) (fileDACL, error) {
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fileDACL{}, err
	}
	control, _, err := sd.Control()
	if err != nil {
		return fileDACL{}, err
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		// A missing DACL grants everything; the sandbox refuses to guess what
		// the owner meant and treats it like an empty one it may extend.
		return fileDACL{control: control, null: true}, nil
	}
	out := fileDACL{control: control}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return fileDACL{}, err
		}
		size := int(ace.Header.AceSize)
		out.aces = append(out.aces, slices.Clone(unsafe.Slice((*byte)(unsafe.Pointer(ace)), size)))
	}
	return out, nil
}

func writeDACL(h windows.Handle, d fileDACL) error {
	size := 8
	for _, a := range d.aces {
		size += len(a)
	}
	buf := make([]uint32, (size+3)/4)
	raw := unsafe.Slice((*byte)(unsafe.Pointer(&buf[0])), len(buf)*4)
	raw[0] = 2 // ACL_REVISION
	binary.LittleEndian.PutUint16(raw[2:4], uint16(size))
	binary.LittleEndian.PutUint16(raw[4:6], uint16(len(d.aces)))
	at := 8
	for _, a := range d.aces {
		at += copy(raw[at:], a)
	}
	sd, err := windows.NewSecurityDescriptor()
	if err != nil {
		return err
	}
	if err := sd.SetDACL((*windows.ACL)(unsafe.Pointer(&buf[0])), true, false); err != nil {
		return err
	}
	keep := windows.SECURITY_DESCRIPTOR_CONTROL(seDACLProtected | seDACLAutoInheri)
	if err := sd.SetControl(keep, d.control&keep); err != nil {
		return err
	}
	return windows.SetKernelObjectSecurity(h, windows.DACL_SECURITY_INFORMATION, sd)
}

// explicitEnd is the index of the first inherited entry.
func (d fileDACL) explicitEnd() int {
	for i, a := range d.aces {
		if a.flags()&aceInherited != 0 {
			return i
		}
	}
	return len(d.aces)
}

func (d fileDACL) has(want rawACE) bool {
	return slices.ContainsFunc(d.aces, func(a rawACE) bool { return string(a) == string(want) })
}

// insert adds entries in canonical order: explicit denies first, explicit
// allows at the end of the explicit part, inherited entries at the start of
// the inherited part (they come from the closest ancestor the sandbox
// changed).
func (d *fileDACL) insert(aces ...rawACE) {
	for _, a := range aces {
		if d.has(a) {
			continue
		}
		at := 0
		switch {
		case a.flags()&aceInherited != 0:
			at = d.explicitEnd()
		case a.typ() == accessAllowedACEType:
			at = d.explicitEnd()
		}
		d.aces = slices.Insert(d.aces, at, a)
	}
}

// remove drops every entry for which drop returns true and reports whether
// anything changed.
func (d *fileDACL) remove(drop func(rawACE) bool) bool {
	n := len(d.aces)
	d.aces = slices.DeleteFunc(d.aces, func(a rawACE) bool { return drop(a) })
	return len(d.aces) != n
}

// aclTarget is one of the sandbox's ACL changes: the explicit entries for the
// top path and the matching inherited entries for what is below it.
type aclTarget struct {
	explicit       []rawACE
	inheritedDirs  []rawACE
	inheritedFiles []rawACE
}

// grantTarget builds the entries for sids with mask on a tree. The top gets
// the mask without DELETE when noDeleteTop is set (a writable root may not be
// removed or renamed by the command), and an inherit-only entry with the full
// mask for its descendants.
func grantTarget(typ byte, mask uint32, noDeleteTop, dir bool, sids ...string) (aclTarget, error) {
	var t aclTarget
	for _, sid := range sids {
		if !dir {
			ace, err := newACE(typ, 0, mask, sid)
			if err != nil {
				return t, err
			}
			t.explicit = append(t.explicit, ace)
			continue
		}
		top := mask
		if noDeleteTop {
			top = mask &^ windows.DELETE
		}
		var list []rawACE
		if top == mask {
			ace, err := newACE(typ, aceObjectInherit|aceContainerInherit, mask, sid)
			if err != nil {
				return t, err
			}
			list = append(list, ace)
		} else {
			self, err := newACE(typ, 0, top, sid)
			if err != nil {
				return t, err
			}
			below, err := newACE(typ, aceObjectInherit|aceContainerInherit|aceInheritOnly, mask, sid)
			if err != nil {
				return t, err
			}
			list = append(list, self, below)
		}
		t.explicit = append(t.explicit, list...)
		d, err := newACE(typ, aceInherited|aceObjectInherit|aceContainerInherit, mask, sid)
		if err != nil {
			return t, err
		}
		f, err := newACE(typ, aceInherited, mask, sid)
		if err != nil {
			return t, err
		}
		t.inheritedDirs = append(t.inheritedDirs, d)
		t.inheritedFiles = append(t.inheritedFiles, f)
	}
	return t, nil
}

// aclWalk is how applyTree treats descendants.
type aclWalk int

const (
	// aclWalkGrant (writable roots, readable tools) skips files with more
	// than one hard link (the other names may lie outside the tree) and
	// descendants that do not inherit; failures leave the descendant
	// inaccessible, which fails closed.
	aclWalkGrant aclWalk = iota
	// aclWalkDeny (protected and masked paths) gives a descendant that does
	// not inherit an explicit entry; every failure is an error, since a
	// descendant left alone stays writable or readable.
	aclWalkDeny
)

// aclMu serializes the sandbox's ACL changes within this process.
var aclMu sync.Mutex

// sandboxACE reports whether a is an entry of the setup described by n.
func (n setupNames) sandboxACE(groupSIDs []string) func(rawACE) bool {
	prefix := n.sidPrefix()
	return func(a rawACE) bool {
		sid := a.sid()
		return sid != "" && (strings.HasPrefix(sid, prefix) || slices.Contains(groupSIDs, sid))
	}
}

// openForDACL opens path itself (never a link's target) to read and change
// its DACL; reading its attributes (GetFileInformationByHandle) needs
// FILE_READ_ATTRIBUTES as well.
func openForDACL(path string) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
}

// applyTree gives top the explicit entries of t and its descendants the
// inherited ones. ours tells the sandbox's entries apart (boundaries of other
// targets). When top already has all of its entries the tree is left alone
// unless force is set: the descendants created since inherited them.
func applyTree(top string, t aclTarget, walk aclWalk, ours func(rawACE) bool, force bool) error {
	aclMu.Lock()
	defer aclMu.Unlock()
	info, err := os.Lstat(top)
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 || isNameSurrogate(top) {
		return fmt.Errorf("sandbox refuses %s: it is a link or junction", top)
	}
	h, err := openForDACL(top)
	if err != nil {
		return fmt.Errorf("change the access of %s: %w", top, err)
	}
	d, err := readDACL(h)
	if err == nil && !force && !d.null && !slices.ContainsFunc(t.explicit, func(a rawACE) bool { return !d.has(a) }) {
		windows.CloseHandle(h)
		return nil
	}
	if err == nil {
		d.insert(t.explicit...)
		err = writeDACL(h, d)
	}
	windows.CloseHandle(h)
	if err != nil {
		return fmt.Errorf("change the access of %s: %w", top, err)
	}
	if !info.IsDir() {
		return nil
	}
	// A descendant with an explicit allow entry of the sandbox is the top of
	// another target (a nested root, a workspace inside a masked directory):
	// its subtree is left to that target.
	boundary := func(a rawACE) bool {
		return a.flags()&aceInherited == 0 && a.typ() == accessAllowedACEType && ours(a)
	}
	return filepath.WalkDir(top, func(path string, entry fs.DirEntry, walkErr error) error {
		if path == top {
			return walkErr
		}
		skip := func(err error) error {
			if err != nil && walk == aclWalkDeny {
				return fmt.Errorf("change the access of %s: %w", path, err)
			}
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if walkErr != nil {
			return skip(walkErr)
		}
		if entry.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return skip(nil)
		}
		h, err := openForDACL(path)
		if err != nil {
			return skip(err)
		}
		defer windows.CloseHandle(h)
		var fi windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(h, &fi); err != nil {
			return skip(err)
		}
		dir := fi.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
		if fi.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return skip(nil)
		}
		if walk == aclWalkGrant && !dir && fi.NumberOfLinks > 1 {
			return skip(nil)
		}
		d, err := readDACL(h)
		if err != nil {
			return skip(err)
		}
		if slices.ContainsFunc(d.aces, boundary) {
			return skip(nil)
		}
		var add []rawACE
		protected := d.control&seDACLProtected != 0
		switch {
		case protected && walk == aclWalkGrant:
			return skip(nil)
		case protected:
			add = t.explicit
		case dir:
			add = t.inheritedDirs
		default:
			add = t.inheritedFiles
		}
		if !slices.ContainsFunc(add, func(a rawACE) bool { return !d.has(a) }) {
			return nil
		}
		d.insert(add...)
		if err := writeDACL(h, d); err != nil {
			return skip(err)
		}
		return nil
	})
}

// removeTree removes every entry ours matches from path and everything below
// it (links and junctions are neither changed nor entered). Paths that no
// longer exist are skipped; other failures are collected.
func removeTree(top string, ours func(rawACE) bool) error {
	aclMu.Lock()
	defer aclMu.Unlock()
	if _, err := os.Lstat(top); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	var errs []error
	clean := func(path string) {
		h, err := openForDACL(path)
		if err != nil {
			if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
				errs = append(errs, fmt.Errorf("%s: %w", path, err))
			}
			return
		}
		defer windows.CloseHandle(h)
		d, err := readDACL(h)
		if err == nil && d.remove(ours) {
			err = writeDACL(h, d)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
		}
	}
	if isNameSurrogate(top) {
		clean(top)
		return errors.Join(errs...)
	}
	_ = filepath.WalkDir(top, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("%s: %w", path, err))
			}
			return nil
		}
		clean(path)
		if path != top && entry.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return filepath.SkipDir
		}
		return nil
	})
	return errors.Join(errs...)
}

// aclRecordMu serializes appends to the record of changed paths.
var aclRecordMu sync.Mutex

var aclRecorded = map[string]bool{}

// recordACLRoot appends path to the record uninstall reads (once per process
// and path). The record is created with the state file's access: the user,
// administrators and SYSTEM, never the sandbox accounts.
func recordACLRoot(st *setupState, path string) error {
	aclRecordMu.Lock()
	defer aclRecordMu.Unlock()
	key := strings.ToLower(path)
	if aclRecorded[key] {
		return nil
	}
	file := filepath.Join(filepath.Dir(st.path), aclRootsFile)
	existing, _ := readACLRoots(file)
	if !slices.ContainsFunc(existing, func(p string) bool { return strings.EqualFold(p, path) }) {
		f, err := openPrivateFile(file, st.UserSID, st.Group.SID, windows.FILE_APPEND_DATA|windows.SYNCHRONIZE, windows.OPEN_ALWAYS)
		if err != nil {
			return fmt.Errorf("record sandbox access entries: %w", err)
		}
		_, err = f.WriteString(path + "\n")
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("record sandbox access entries: %w", err)
		}
	}
	aclRecorded[key] = true
	return nil
}

func readACLRoots(file string) ([]string, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" && filepath.IsAbs(line) && !slices.Contains(out, line) {
			out = append(out, line)
		}
	}
	return out, sc.Err()
}

// openPrivateFile creates or opens file with a protected DACL that grants the
// user, administrators and SYSTEM full access and denies the sandbox group
// everything.
func openPrivateFile(file, userSID, groupSID string, access uint32, disposition uint32) (*os.File, error) {
	sddl := fmt.Sprintf("D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;%s)", userSID)
	if groupSID != "" {
		sddl = fmt.Sprintf("D:P(D;;FA;;;%s)(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;%s)", groupSID, userSID)
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	name, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, access, windows.FILE_SHARE_READ, &sa, disposition, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), file), nil
}

// sandboxMaskVerified is when a masked tree last got its deny entries.
var sandboxMaskVerified = map[string]time.Time{}
