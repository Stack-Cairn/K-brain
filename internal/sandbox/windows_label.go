//go:build windows

package sandbox

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Mandatory integrity labels. A low-integrity process may write an object
// only if the object's label is low (or lower); an unlabelled object counts as
// medium with no-write-up. A label can also forbid reading and executing by
// lower-integrity processes. The sandbox labels its writable roots and fenced
// temp directory low, inherited by everything below them, protected paths
// inside a root medium, and masked directories medium with no-read-up, so the
// command can write the first, only read the second and not open the third.
const (
	lowIntegrityRID    = 0x1000
	mediumIntegrityRID = 0x2000

	systemMandatoryLabelACEType = 0x11
	mandatoryLabelNoWriteUp     = 0x1
	mandatoryLabelNoReadUp      = 0x2
	mandatoryLabelNoExecuteUp   = 0x4
	aceObjectInherit            = 0x1
	aceContainerInherit         = 0x2
)

// integrityLabel is a mandatory label: an integrity level and the accesses it
// denies to lower-integrity processes.
type integrityLabel struct {
	rid    uint32
	policy uint32
}

var (
	writableLabel  = integrityLabel{lowIntegrityRID, mandatoryLabelNoWriteUp}
	protectedLabel = integrityLabel{mediumIntegrityRID, mandatoryLabelNoWriteUp}
	maskedLabel    = integrityLabel{mediumIntegrityRID, mandatoryLabelNoWriteUp | mandatoryLabelNoReadUp | mandatoryLabelNoExecuteUp}
)

// satisfies reports whether an existing label l gives what want asks for: a
// writable label must be exactly low, a medium one at least as high and at
// least as strict.
func (l integrityLabel) satisfies(want integrityLabel) bool {
	if want.rid == lowIntegrityRID {
		return l.rid == lowIntegrityRID && l.policy&mandatoryLabelNoWriteUp != 0
	}
	return l.rid >= want.rid && l.policy&want.policy == want.policy
}

// labelMu serializes the read-modify-write of labels within this process.
var labelMu sync.Mutex

// maskRecheck is how long a masked tree counts as verified. Files created in
// a masked directory inherit its label, but a file moved in from elsewhere
// keeps its own, so the tree is walked again once this has passed.
var maskRecheck = time.Minute

var maskVerified = map[string]time.Time{}

// ensureMasked gives the existing directory or file path the masked label,
// on path and every descendant (links and junctions are neither labelled nor
// entered). Any descendant that cannot be labelled is an error.
func ensureMasked(path string) error {
	labelMu.Lock()
	defer labelMu.Unlock()
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if at, ok := maskVerified[path]; ok && time.Since(at) < maskRecheck {
		if current, flags, explicit, err := fileLabel(path); err == nil && explicit && flags&(aceObjectInherit|aceContainerInherit) == inheritFlags(info.IsDir()) && current.satisfies(maskedLabel) {
			return nil
		}
	}
	if err := labelTree(path, info.IsDir(), maskedLabel, walkMask); err != nil {
		return err
	}
	maskVerified[path] = time.Now()
	return nil
}

func inheritFlags(dir bool) uint8 {
	if dir {
		return aceObjectInherit | aceContainerInherit
	}
	return 0
}

// ensureLabel gives path an explicit label want (writableLabel or
// protectedLabel), inherited by its children when path is a directory, unless
// it already has one that satisfies want. Labels are never removed: they stay
// for the next run, and removing them would race with concurrent runs on the
// same root.
//
// The label is propagated to the existing children by labelTree rather than by
// SetNamedSecurityInfo, so that propagation never follows a junction or
// symbolic link out of the tree.
func ensureLabel(path string, want integrityLabel) error {
	labelMu.Lock()
	defer labelMu.Unlock()
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	current, flags, explicit, err := fileLabel(path)
	if err != nil {
		return fmt.Errorf("read the integrity label of %s: %w", path, err)
	}
	if explicit && flags&(aceObjectInherit|aceContainerInherit) == inheritFlags(info.IsDir()) && current.satisfies(want) {
		return nil
	}
	mode := walkProtect
	if want.rid == lowIntegrityRID {
		mode = walkLower
	}
	return labelTree(path, info.IsDir(), want, mode)
}

// walkMode is how labelTree treats the descendants of the top path.
type walkMode int

const (
	// walkLower (writable roots) skips descendants with an explicit label and
	// files with more than one hard link (the other names may lie outside the
	// tree); a descendant that cannot be labelled simply stays unwritable.
	walkLower walkMode = iota
	// walkProtect (protected paths) skips descendants with an explicit label;
	// every failure is an error, since a descendant left low stays writable.
	walkProtect
	// walkMask (masked directories) replaces every label that does not
	// satisfy the masked label, explicit or not, and descends everywhere;
	// every failure is an error, since a descendant left alone stays readable.
	walkMask
)

// labelTree sets the explicit label on top and the matching inherited label on
// its descendants as mode says. Links and junctions are neither labelled nor
// entered.
func labelTree(top string, dir bool, want integrityLabel, mode walkMode) error {
	if mode == walkMask {
		// Never lower a label that is already above medium.
		if current, _, _, err := fileLabel(top); err == nil && current.rid > want.rid {
			want.rid = current.rid
		}
	}
	if err := setLabel(top, want, dir, false); err != nil {
		return fmt.Errorf("label %s: %w", top, err)
	}
	if !dir {
		return nil
	}
	lowering := mode == walkLower
	return filepath.WalkDir(top, func(path string, d fs.DirEntry, walkErr error) error {
		if path == top {
			return walkErr
		}
		skip := func(err error) error {
			if err != nil && !lowering {
				return fmt.Errorf("label %s: %w", path, err)
			}
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if walkErr != nil {
			return skip(walkErr)
		}
		if d.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return skip(nil)
		}
		h, err := openForLabel(path)
		if err != nil {
			return skip(err)
		}
		defer windows.CloseHandle(h)
		var fi windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(h, &fi); err != nil {
			return skip(err)
		}
		isDir := fi.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
		if fi.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (lowering && !isDir && fi.NumberOfLinks > 1) {
			return skip(nil)
		}
		sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.LABEL_SECURITY_INFORMATION)
		if err != nil {
			return skip(err)
		}
		current, _, explicit := labelOf(sd)
		label := want
		if mode == walkMask {
			if current.satisfies(want) {
				// Already hidden (inherited, or an explicit label at least as
				// strict); its own descendants are still checked.
				return nil
			}
			if current.rid > label.rid {
				label.rid = current.rid
			}
		} else if explicit {
			return skip(nil)
		}
		desc, err := labelDescriptor(label, isDir, true)
		if err == nil {
			err = windows.SetKernelObjectSecurity(h, windows.LABEL_SECURITY_INFORMATION, desc)
		}
		if err != nil {
			return skip(err)
		}
		return nil
	})
}

func setLabel(path string, want integrityLabel, dir, inherited bool) error {
	h, err := openForLabel(path)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	label, err := labelDescriptor(want, dir, inherited)
	if err != nil {
		return err
	}
	return windows.SetKernelObjectSecurity(h, windows.LABEL_SECURITY_INFORMATION, label)
}

// openForLabel opens path itself (never a link's target) to read and change
// its label.
func openForLabel(path string) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_OWNER, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
}

// labelDescriptor is a security descriptor holding one mandatory label ACE.
func labelDescriptor(l integrityLabel, dir, inherited bool) (*windows.SECURITY_DESCRIPTOR, error) {
	flags := ""
	if inherited {
		flags = "ID"
	}
	if dir {
		flags += "OICI"
	}
	policy := ""
	for _, p := range []struct {
		bit  uint32
		sddl string
	}{{mandatoryLabelNoWriteUp, "NW"}, {mandatoryLabelNoReadUp, "NR"}, {mandatoryLabelNoExecuteUp, "NX"}} {
		if l.policy&p.bit != 0 {
			policy += p.sddl
		}
	}
	return windows.SecurityDescriptorFromString(fmt.Sprintf("S:(ML;%s;%s;;;S-1-16-%d)", flags, policy, l.rid))
}

// fileLabel returns the label that applies to path (its first mandatory label
// ACE, or medium no-write-up when it has none), that ACE's flags and whether
// it is explicit rather than inherited.
func fileLabel(path string) (label integrityLabel, flags uint8, explicit bool, err error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.LABEL_SECURITY_INFORMATION)
	if err != nil {
		return integrityLabel{}, 0, false, err
	}
	label, flags, explicit = labelOf(sd)
	return label, flags, explicit, nil
}

func labelOf(sd *windows.SECURITY_DESCRIPTOR) (label integrityLabel, flags uint8, explicit bool) {
	sacl, _, err := sd.SACL()
	if err != nil || sacl == nil {
		return protectedLabel, 0, false
	}
	for i := uint32(0); i < uint32(sacl.AceCount); i++ {
		// A mandatory label ACE has the layout of an allow ACE.
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(sacl, i, &ace) != nil || ace.Header.AceType != systemMandatoryLabelACEType {
			continue
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sid.SubAuthorityCount() == 0 {
			continue
		}
		level := sid.SubAuthority(uint32(sid.SubAuthorityCount()) - 1)
		return integrityLabel{level, uint32(ace.Mask)}, ace.Header.AceFlags, ace.Header.AceFlags&windows.INHERITED_ACE == 0
	}
	return protectedLabel, 0, false
}

// isNameSurrogate reports whether path is a symbolic link or junction (a
// reparse point that names another file). Placeholders of cloud-synced folders
// are reparse points too but not name surrogates, so they stay usable. When the
// tag cannot be read the path is treated as a link.
func isNameSurrogate(path string) bool {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return true
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil || attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
		return false
	}
	var data windows.Win32finddata
	h, err := windows.FindFirstFile(p, &data)
	if err != nil {
		return true
	}
	_ = windows.FindClose(h)
	const nameSurrogate = 0x20000000
	return data.Reserved0&nameSurrogate != 0
}
