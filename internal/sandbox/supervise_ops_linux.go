//go:build linux

package sandbox

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The kernel's struct seccomp_notif, seccomp_notif_resp and
// seccomp_notif_addfd.
type notifData struct {
	Nr   int32
	Arch uint32
	IP   uint64
	Args [6]uint64
}

type notifRequest struct {
	ID    uint64
	Pid   uint32
	Flags uint32
	Data  notifData
}

type notifResponse struct {
	ID    uint64
	Val   int64
	Error int32
	Flags uint32
}

type notifAddFD struct {
	ID         uint64
	Flags      uint32
	SrcFD      uint32
	NewFD      uint32
	NewFDFlags uint32
}

// openTmpfile is __O_TMPFILE, the bit O_TMPFILE adds to O_DIRECTORY.
const openTmpfile = 0x400000

type supervisor struct {
	plan     *protectPlan
	listener int
	calls    fsCalls
}

// reply is the supervisor's answer to one call: continue it in the kernel,
// fail it with errno, return val, or install fd in the caller and return its
// number.
type reply struct {
	cont    bool
	errno   unix.Errno
	val     int64
	fd      int
	fdFlags uint32
}

var continueCall = reply{cont: true, fd: -1}

func fail(errno unix.Errno) reply { return reply{errno: errno, fd: -1} }

func result(err error) reply {
	if err == nil {
		return reply{fd: -1}
	}
	var errno unix.Errno
	if errors.As(err, &errno) {
		return fail(errno)
	}
	return fail(unix.EIO)
}

func (s *supervisor) serve() error {
	for {
		var req notifRequest
		if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(s.listener), uintptr(unix.SECCOMP_IOCTL_NOTIF_RECV), uintptr(unsafe.Pointer(&req))); errno != 0 {
			// EINTR: a signal; ENOENT: the caller died before the call was
			// received.
			if errno == unix.EINTR || errno == unix.ENOENT {
				continue
			}
			return errno
		}
		call := &call{s: s, id: req.ID, tid: int(req.Pid), args: req.Data.Args}
		r := s.handle(call, uint32(req.Data.Nr))
		s.respond(req.ID, r)
	}
}

func (s *supervisor) respond(id uint64, r reply) {
	if r.fd >= 0 {
		add := notifAddFD{ID: id, Flags: unix.SECCOMP_ADDFD_FLAG_SEND, SrcFD: uint32(r.fd), NewFDFlags: r.fdFlags}
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(s.listener), uintptr(unix.SECCOMP_IOCTL_NOTIF_ADDFD), uintptr(unsafe.Pointer(&add)))
		unix.Close(r.fd)
		// ENOENT: the caller died; EINPROGRESS: the call is answered.
		if errno == 0 || errno == unix.ENOENT || errno == unix.EINPROGRESS {
			return
		}
		// The descriptor could not be installed (EMFILE): fail the call.
		r = fail(errno)
	}
	resp := notifResponse{ID: id}
	switch {
	case r.cont:
		resp.Flags = unix.SECCOMP_USER_NOTIF_FLAG_CONTINUE
	case r.errno != 0:
		resp.Error = -int32(r.errno)
	default:
		resp.Val = r.val
	}
	_, _, _ = unix.Syscall(unix.SYS_IOCTL, uintptr(s.listener), uintptr(unix.SECCOMP_IOCTL_NOTIF_SEND), uintptr(unsafe.Pointer(&resp)))
}

// call is one notification being handled.
type call struct {
	s    *supervisor
	id   uint64
	tid  int
	args [6]uint64
}

// valid reports whether the notification is still pending, so what was read
// from /proc/<tid> belongs to the caller and not to a process that reused its
// id. It is checked after reading and before acting.
func (c *call) valid() bool {
	id := c.id
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(c.s.listener), uintptr(unix.SECCOMP_IOCTL_NOTIF_ID_VALID), uintptr(unsafe.Pointer(&id)))
	return errno == 0
}

func (c *call) fd(i int) int32 { return int32(uint32(c.args[i])) }

// str reads a NUL-terminated path from the caller's memory.
func (c *call) str(i int) (string, error) {
	addr := uintptr(c.args[i])
	if addr == 0 {
		return "", unix.EFAULT
	}
	const max = unix.PathMax
	page := uintptr(os.Getpagesize())
	var out []byte
	for len(out) < max {
		chunk := int(page - addr%page)
		if chunk > max-len(out) {
			chunk = max - len(out)
		}
		buf := make([]byte, chunk)
		n, err := unix.ProcessVMReadv(c.tid, []unix.Iovec{{Base: &buf[0], Len: uint64(chunk)}}, []unix.RemoteIovec{{Base: addr, Len: chunk}}, 0)
		if err != nil {
			return "", err
		}
		if n <= 0 {
			return "", unix.EFAULT
		}
		if i := bytes.IndexByte(buf[:n], 0); i >= 0 {
			return string(append(out, buf[:i]...)), nil
		}
		out = append(out, buf[:n]...)
		addr += uintptr(n)
	}
	return "", unix.ENAMETOOLONG
}

func (c *call) bytes(i int, size int) ([]byte, error) {
	buf := make([]byte, size)
	n, err := unix.ProcessVMReadv(c.tid, []unix.Iovec{{Base: &buf[0], Len: uint64(size)}}, []unix.RemoteIovec{{Base: uintptr(c.args[i]), Len: size}}, 0)
	if err != nil {
		return nil, err
	}
	if n != size {
		return nil, unix.EFAULT
	}
	return buf, nil
}

// status returns a field of /proc/<tid>/status.
func (c *call) status(field string) string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(c.tid) + "/status")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, field+":"); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (c *call) umask() uint32 {
	if mask, err := strconv.ParseUint(c.status("Umask"), 8, 32); err == nil {
		return uint32(mask)
	}
	return 0o022
}

// start opens the directory a path of the caller is resolved from: its root
// for an absolute path, its working directory for AT_FDCWD, else dirfd.
func (c *call) start(dirfd int32, abs bool) (int, error) {
	proc := "/proc/" + strconv.Itoa(c.tid) + "/"
	switch {
	case abs:
		proc += "root"
	case dirfd == unix.AT_FDCWD:
		proc += "cwd"
	default:
		proc += "fd/" + strconv.Itoa(int(dirfd))
	}
	return unix.Open(proc, unix.O_PATH|unix.O_CLOEXEC, 0)
}

// resolve opens path the way the caller would see it, without magic links
// (a /proc/self in the path would name the supervisor).
func (c *call) resolve(dirfd int32, path string, flags uint64) (int, error) {
	abs := strings.HasPrefix(path, "/")
	start, err := c.start(dirfd, abs)
	if err != nil {
		return -1, err
	}
	defer unix.Close(start)
	how := unix.OpenHow{Flags: flags | unix.O_PATH | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_MAGICLINKS}
	if abs {
		how.Resolve |= unix.RESOLVE_IN_ROOT
	}
	if path == "" {
		path = "."
	}
	return unix.Openat2(start, path, &how)
}

// entry is a path split into its parent directory (an O_PATH descriptor) and
// its last name.
type entry struct {
	dir      int
	dirKey   fileKey
	class    pathClass
	name     string
	trailing bool
}

func (e *entry) close() {
	if e != nil && e.dir >= 0 {
		unix.Close(e.dir)
	}
}

// errNotHandled makes the supervisor continue a call it does not emulate.
var errNotHandled = errors.New("not handled")

func (c *call) parent(dirfd int32, path string) (*entry, error) {
	trimmed := strings.TrimRight(path, "/")
	if trimmed == "" {
		return nil, errNotHandled
	}
	dir, name := ".", trimmed
	if i := strings.LastIndexByte(trimmed, '/'); i >= 0 {
		dir, name = trimmed[:i], trimmed[i+1:]
		if dir == "" {
			dir = "/"
		}
	}
	if name == "." || name == ".." {
		return nil, errNotHandled
	}
	fd, err := c.resolve(dirfd, dir, unix.O_DIRECTORY)
	if err != nil {
		return nil, errNotHandled
	}
	class, key, err := c.s.classify(fd)
	if err != nil {
		unix.Close(fd)
		return nil, errNotHandled
	}
	return &entry{dir: fd, dirKey: key, class: class, name: name, trailing: len(trimmed) < len(path)}, nil
}

type pathClass int

const (
	// classOutside: beyond every grant; the command's domain decides.
	classOutside pathClass = iota
	// classNative: beneath a directory with a write rule.
	classNative
	// classEmulate: a locked directory, or one created beneath it during the
	// run; only the supervisor can change its entries.
	classEmulate
	// classProtected: a protected path or beneath one.
	classProtected
)

// classify walks up from the directory fd to the first inode the plan knows.
// The protected trees and the locked directories cannot move during the run,
// so a directory beneath them is always found beneath them.
func (s *supervisor) classify(fd int) (pathClass, fileKey, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return classOutside, fileKey{}, err
	}
	self := keyOf(&st)
	key, current := self, fd
	defer func() {
		if current != fd {
			unix.Close(current)
		}
	}()
	for depth := 0; depth < 4096; depth++ {
		switch {
		case s.plan.protected[key]:
			return classProtected, self, nil
		case s.plan.rules[key]:
			return classNative, self, nil
		case s.plan.locked[key]:
			return classEmulate, self, nil
		}
		up, err := unix.Openat(current, "..", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return classOutside, self, err
		}
		if current != fd {
			unix.Close(current)
		}
		current = up
		if err := unix.Fstat(current, &st); err != nil {
			return classOutside, self, err
		}
		if keyOf(&st) == key {
			return classOutside, self, nil
		}
		key = keyOf(&st)
	}
	return classOutside, self, nil
}

// lookup stats the entry and reports whether the supervisor may change it:
// not a reserved name, not a locked directory or protected path.
func (s *supervisor) lookup(e *entry) (exists bool, st unix.Stat_t, mutable bool) {
	if s.plan.reserved[e.dirKey][e.name] {
		return false, st, false
	}
	if err := unix.Fstatat(e.dir, e.name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false, st, errors.Is(err, unix.ENOENT)
	}
	key := keyOf(&st)
	if s.plan.locked[key] || s.plan.protected[key] || s.plan.frozen[key] {
		return true, st, false
	}
	return true, st, true
}

func (s *supervisor) handle(c *call, nr uint32) reply {
	k := s.calls
	at := int32(unix.AT_FDCWD)
	switch {
	case nr == k.openat:
		return c.open(c.fd(0), 1, c.args[2], c.args[3])
	case nr == k.open && k.open != 0:
		return c.open(at, 0, c.args[1], c.args[2])
	case nr == k.creat && k.creat != 0:
		return c.open(at, 0, unix.O_CREAT|unix.O_WRONLY|unix.O_TRUNC, c.args[1])
	case nr == k.openat2:
		return c.openat2()
	case nr == k.mkdirat:
		return c.mknod(c.fd(0), 1, unix.S_IFDIR|(c.args[2]&0o7777))
	case nr == k.mkdir && k.mkdir != 0:
		return c.mknod(at, 0, unix.S_IFDIR|(c.args[1]&0o7777))
	case nr == k.mknodat:
		return c.mknod(c.fd(0), 1, c.args[2])
	case nr == k.mknod && k.mknod != 0:
		return c.mknod(at, 0, c.args[1])
	case nr == k.unlinkat:
		return c.unlink(c.fd(0), 1, int(c.args[2]))
	case nr == k.unlink && k.unlink != 0:
		return c.unlink(at, 0, 0)
	case nr == k.rmdir && k.rmdir != 0:
		return c.unlink(at, 0, unix.AT_REMOVEDIR)
	case nr == k.renameat2:
		return c.rename(c.fd(0), 1, c.fd(2), 3, uint(c.args[4]))
	case nr == k.renameat && k.renameat != 0:
		return c.rename(c.fd(0), 1, c.fd(2), 3, 0)
	case nr == k.rename && k.rename != 0:
		return c.rename(at, 0, at, 1, 0)
	case nr == k.linkat:
		return c.link(c.fd(0), 1, c.fd(2), 3, int(c.args[4]))
	case nr == k.link && k.link != 0:
		return c.link(at, 0, at, 1, 0)
	case nr == k.symlinkat:
		return c.symlink(0, c.fd(1), 2)
	case nr == k.symlink && k.symlink != 0:
		return c.symlink(0, at, 1)
	case nr == k.truncate:
		return c.truncate()
	case nr == k.fchmodat2:
		return c.chmodPath(c.fd(0), 1, modeChange(c.args[2]), int(c.args[3]))
	case nr == k.fchmodat:
		return c.chmodPath(c.fd(0), 1, modeChange(c.args[2]), 0)
	case nr == k.chmod && k.chmod != 0:
		return c.chmodPath(at, 0, modeChange(c.args[1]), 0)
	case nr == k.fchmod:
		return c.chmodFD(int(c.fd(0)), modeChange(c.args[1]), false)
	case nr == k.fchownat:
		return c.chmodPath(c.fd(0), 1, c.ownerChange(2), int(c.args[4]))
	case nr == k.chown && k.chown != 0:
		return c.chmodPath(at, 0, c.ownerChange(1), 0)
	case nr == k.lchown && k.lchown != 0:
		return c.chmodPath(at, 0, c.ownerChange(1), unix.AT_SYMLINK_NOFOLLOW)
	case nr == k.fchown:
		return c.chmodFD(int(c.fd(0)), c.ownerChange(1), false)
	}
	return continueCall
}

func (c *call) openat2() reply {
	size := int(c.args[3])
	if size < 24 {
		return continueCall
	}
	raw, err := c.bytes(2, 24)
	if err != nil {
		return continueCall
	}
	how := (*unix.OpenHow)(unsafe.Pointer(&raw[0]))
	if how.Resolve != 0 {
		// Resolution limits are the caller's own; the kernel applies them
		// under the command's domain.
		return continueCall
	}
	return c.open(c.fd(0), 1, how.Flags, how.Mode)
}

func (c *call) open(dirfd int32, pathArg int, flags, mode uint64) reply {
	if flags&unix.O_PATH != 0 || flags&openWriteFlags == 0 {
		return continueCall
	}
	path, err := c.str(pathArg)
	if err != nil {
		return continueCall
	}
	cloexec := uint32(0)
	if flags&unix.O_CLOEXEC != 0 {
		cloexec = unix.O_CLOEXEC
	}
	perm := uint32(mode) & 0o7777
	if flags&openTmpfile != 0 {
		dir, err := c.resolve(dirfd, path, unix.O_DIRECTORY)
		if err != nil {
			return continueCall
		}
		defer unix.Close(dir)
		if class, _, err := c.s.classify(dir); err != nil || class != classEmulate {
			return continueCall
		}
		umask := c.umask()
		if !c.valid() {
			return continueCall
		}
		fd, err := unix.Openat(dir, ".", int(flags)|unix.O_CLOEXEC, perm&^umask)
		if err != nil {
			return result(err)
		}
		return reply{fd: fd, fdFlags: cloexec}
	}
	e, err := c.parent(dirfd, path)
	if err != nil {
		return continueCall
	}
	defer e.close()
	if e.class != classEmulate || e.trailing {
		return continueCall
	}
	exists, st, mutable := c.s.lookup(e)
	if !mutable {
		return continueCall
	}
	if exists {
		if kind := st.Mode & unix.S_IFMT; kind != unix.S_IFREG && kind != unix.S_IFDIR {
			// Symlinks, FIFOs and devices: the kernel decides.
			return continueCall
		}
	} else if flags&unix.O_CREAT == 0 {
		return continueCall
	}
	umask := c.umask()
	if !c.valid() {
		return continueCall
	}
	fd, err := unix.Openat(e.dir, e.name, int(flags)|unix.O_NOFOLLOW|unix.O_CLOEXEC, perm&^umask)
	if err != nil {
		return result(err)
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil || c.s.plan.frozen[keyOf(&opened)] || c.s.plan.protected[keyOf(&opened)] {
		unix.Close(fd)
		return fail(unix.EACCES)
	}
	return reply{fd: fd, fdFlags: cloexec}
}

// mknod handles mkdir (mode carries S_IFDIR) and mknod of regular files,
// FIFOs and sockets.
func (c *call) mknod(dirfd int32, pathArg int, mode uint64) reply {
	kind := uint32(mode) & unix.S_IFMT
	switch kind {
	case 0, unix.S_IFREG, unix.S_IFIFO, unix.S_IFSOCK, unix.S_IFDIR:
	default:
		return continueCall
	}
	path, err := c.str(pathArg)
	if err != nil {
		return continueCall
	}
	e, err := c.parent(dirfd, path)
	if err != nil {
		return continueCall
	}
	defer e.close()
	if e.class != classEmulate || (e.trailing && kind != unix.S_IFDIR) {
		return continueCall
	}
	exists, _, mutable := c.s.lookup(e)
	if !mutable {
		return continueCall
	}
	if exists {
		return fail(unix.EEXIST)
	}
	perm := uint32(mode) & 0o7777 &^ c.umask()
	if !c.valid() {
		return continueCall
	}
	if kind == unix.S_IFDIR {
		return result(unix.Mkdirat(e.dir, e.name, perm))
	}
	if kind == 0 {
		kind = unix.S_IFREG
	}
	return result(unix.Mknodat(e.dir, e.name, kind|perm, 0))
}

func (c *call) unlink(dirfd int32, pathArg int, flags int) reply {
	if flags&^unix.AT_REMOVEDIR != 0 {
		return continueCall
	}
	path, err := c.str(pathArg)
	if err != nil {
		return continueCall
	}
	e, err := c.parent(dirfd, path)
	if err != nil {
		return continueCall
	}
	defer e.close()
	if e.class != classEmulate || (e.trailing && flags&unix.AT_REMOVEDIR == 0) {
		return continueCall
	}
	exists, _, mutable := c.s.lookup(e)
	if !exists || !mutable || !c.valid() {
		return continueCall
	}
	return result(unix.Unlinkat(e.dir, e.name, flags))
}

func (c *call) rename(oldfd int32, oldArg int, newfd int32, newArg int, flags uint) reply {
	if flags&^(unix.RENAME_NOREPLACE|unix.RENAME_EXCHANGE) != 0 {
		return continueCall
	}
	oldPath, err := c.str(oldArg)
	if err != nil {
		return continueCall
	}
	newPath, err := c.str(newArg)
	if err != nil {
		return continueCall
	}
	src, err := c.parent(oldfd, oldPath)
	if err != nil {
		return continueCall
	}
	defer src.close()
	dst, err := c.parent(newfd, newPath)
	if err != nil {
		return continueCall
	}
	defer dst.close()
	usable := func(class pathClass) bool { return class == classNative || class == classEmulate }
	if !usable(src.class) || !usable(dst.class) || (src.class == classNative && dst.class == classNative) {
		return continueCall
	}
	srcExists, srcStat, srcOK := c.s.lookup(src)
	dstExists, dstStat, dstOK := c.s.lookup(dst)
	if !srcExists || !srcOK || !dstOK {
		return continueCall
	}
	if flags&unix.RENAME_EXCHANGE != 0 {
		if !dstExists {
			return continueCall
		}
		if (dst.trailing || src.trailing) && dstStat.Mode&unix.S_IFMT != unix.S_IFDIR {
			return fail(unix.ENOTDIR)
		}
	}
	if (src.trailing || dst.trailing) && srcStat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fail(unix.ENOTDIR)
	}
	if !c.valid() {
		return continueCall
	}
	return result(unix.Renameat2(src.dir, src.name, dst.dir, dst.name, flags))
}

func (c *call) link(oldfd int32, oldArg int, newfd int32, newArg int, flags int) reply {
	if flags&^unix.AT_SYMLINK_FOLLOW != 0 {
		return continueCall
	}
	oldPath, err := c.str(oldArg)
	if err != nil {
		return continueCall
	}
	newPath, err := c.str(newArg)
	if err != nil {
		return continueCall
	}
	dst, err := c.parent(newfd, newPath)
	if err != nil {
		return continueCall
	}
	defer dst.close()
	if dst.class != classEmulate || dst.trailing {
		return continueCall
	}
	if n, ok := c.procFD(oldPath); ok && flags&unix.AT_SYMLINK_FOLLOW != 0 {
		return c.linkUnnamed(n, dst)
	}
	src, err := c.parent(oldfd, oldPath)
	if err != nil {
		return continueCall
	}
	defer src.close()
	if (src.class != classNative && src.class != classEmulate) || src.trailing {
		return continueCall
	}
	srcExists, srcStat, srcOK := c.s.lookup(src)
	if !srcExists || !srcOK {
		return continueCall
	}
	switch srcStat.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return fail(unix.EPERM)
	case unix.S_IFLNK:
		if flags&unix.AT_SYMLINK_FOLLOW != 0 {
			return continueCall
		}
	}
	dstExists, _, dstOK := c.s.lookup(dst)
	if !dstOK {
		return continueCall
	}
	if dstExists {
		return fail(unix.EEXIST)
	}
	if !c.valid() {
		return continueCall
	}
	// Link the inode that was checked, through its descriptor.
	fd, err := unix.Openat(src.dir, src.name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return result(err)
	}
	defer unix.Close(fd)
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil || keyOf(&opened) != keyOf(&srcStat) {
		return continueCall
	}
	return result(unix.Linkat(unix.AT_FDCWD, "/proc/self/fd/"+strconv.Itoa(fd), dst.dir, dst.name, unix.AT_SYMLINK_FOLLOW))
}

// linkUnnamed gives a name to a file made with O_TMPFILE, linked through
// /proc/self/fd/N. Only a file without links is accepted: the kernel links no
// other such file, and a named file from elsewhere must not get a second name
// in the workspace that the command's domain would let it write.
func (c *call) linkUnnamed(n int, dst *entry) reply {
	exists, _, mutable := c.s.lookup(dst)
	if !mutable {
		return continueCall
	}
	if exists {
		return fail(unix.EEXIST)
	}
	fd, err := c.peerFD(n)
	if err != nil {
		return continueCall
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Nlink != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return continueCall
	}
	if !c.valid() {
		return continueCall
	}
	return result(unix.Linkat(unix.AT_FDCWD, "/proc/self/fd/"+strconv.Itoa(fd), dst.dir, dst.name, unix.AT_SYMLINK_FOLLOW))
}

// peerFD opens the inode behind descriptor n of the caller (O_PATH).
func (c *call) peerFD(n int) (int, error) {
	return unix.Open("/proc/"+strconv.Itoa(c.tid)+"/fd/"+strconv.Itoa(n), unix.O_PATH|unix.O_CLOEXEC, 0)
}

func (c *call) symlink(targetArg int, newfd int32, newArg int) reply {
	target, err := c.str(targetArg)
	if err != nil {
		return continueCall
	}
	path, err := c.str(newArg)
	if err != nil {
		return continueCall
	}
	e, err := c.parent(newfd, path)
	if err != nil {
		return continueCall
	}
	defer e.close()
	if e.class != classEmulate || e.trailing {
		return continueCall
	}
	exists, _, mutable := c.s.lookup(e)
	if !mutable {
		return continueCall
	}
	if exists {
		return fail(unix.EEXIST)
	}
	if !c.valid() {
		return continueCall
	}
	return result(unix.Symlinkat(target, e.dir, e.name))
}

func (c *call) truncate() reply {
	path, err := c.str(0)
	if err != nil {
		return continueCall
	}
	e, err := c.parent(unix.AT_FDCWD, path)
	if err != nil {
		return continueCall
	}
	defer e.close()
	if e.class != classEmulate || e.trailing {
		return continueCall
	}
	exists, st, mutable := c.s.lookup(e)
	if !exists || !mutable || st.Mode&unix.S_IFMT != unix.S_IFREG || !c.valid() {
		return continueCall
	}
	fd, err := unix.Openat(e.dir, e.name, unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return result(err)
	}
	defer unix.Close(fd)
	return result(unix.Ftruncate(fd, int64(c.args[1])))
}

// mayChmod refuses a mode or owner change on a protected path or beneath one. When the
// walk of the protected trees was cut short, a file's place cannot be told
// from its descriptor, so only directories (classified by walking up) may
// change mode.
func (s *supervisor) mayChmod(fd int, st *unix.Stat_t) bool {
	key := keyOf(st)
	if s.plan.frozen[key] || s.plan.protected[key] {
		return false
	}
	if st.Mode&unix.S_IFMT == unix.S_IFDIR {
		class, _, err := s.classify(fd)
		return err == nil && class != classProtected
	}
	return s.plan.frozenAll
}

// procFD matches the /proc paths that name one of the caller's descriptors.
func (c *call) procFD(path string) (int, bool) {
	rest, ok := strings.CutPrefix(path, "/proc/")
	if !ok {
		if rest, ok = strings.CutPrefix(path, "/dev/fd/"); ok {
			n, err := strconv.Atoi(rest)
			return n, err == nil && n >= 0
		}
		return 0, false
	}
	owner, fd, ok := strings.Cut(rest, "/fd/")
	if !ok {
		return 0, false
	}
	switch owner {
	case "self", "thread-self", strconv.Itoa(c.tid), c.status("Tgid"):
	default:
		return 0, false
	}
	n, err := strconv.Atoi(fd)
	return n, err == nil && n >= 0
}

// chmodPath performs chmod, fchmodat and fchmodat2, and the chown family.
// Mode and owner changes are not Landlock rights, so the call is never
// continued: the supervisor resolves the path, refuses a protected inode and
// changes the inode it checked.
func (c *call) chmodPath(dirfd int32, pathArg int, mode attrChange, flags int) reply {
	path, err := c.str(pathArg)
	if err != nil {
		return result(err)
	}
	if flags&^(unix.AT_SYMLINK_NOFOLLOW|unix.AT_EMPTY_PATH) != 0 {
		return fail(unix.EINVAL)
	}
	if path == "" {
		if flags&unix.AT_EMPTY_PATH == 0 {
			return fail(unix.ENOENT)
		}
		if dirfd == unix.AT_FDCWD {
			return c.chmodCwd(mode)
		}
		return c.chmodFD(int(dirfd), mode, true)
	}
	if n, ok := c.procFD(path); ok {
		return c.chmodFD(n, mode, true)
	}
	open := uint64(0)
	if flags&unix.AT_SYMLINK_NOFOLLOW != 0 {
		open = unix.O_NOFOLLOW
	}
	fd, err := c.resolve(dirfd, path, open)
	if err != nil {
		return result(err)
	}
	defer unix.Close(fd)
	return c.chmodOpened(fd, mode)
}

func (c *call) chmodCwd(mode attrChange) reply {
	fd, err := c.start(unix.AT_FDCWD, false)
	if err != nil {
		return result(err)
	}
	defer unix.Close(fd)
	return c.chmodOpened(fd, mode)
}

func (c *call) chmodOpened(fd int, change attrChange) reply {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return result(err)
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK && !change.chown {
		return fail(unix.EOPNOTSUPP)
	}
	if !c.s.mayChmod(fd, &st) {
		return fail(unix.EACCES)
	}
	if !c.valid() {
		return fail(unix.EINTR)
	}
	if change.chown {
		return result(unix.Fchownat(fd, "", change.uid, change.gid, unix.AT_EMPTY_PATH))
	}
	return result(unix.Fchmodat(unix.AT_FDCWD, "/proc/self/fd/"+strconv.Itoa(fd), change.mode, 0))
}

// attrChange is a mode change, or with chown an owner change (-1 keeps the
// id), as chmod and chown share their path handling.
type attrChange struct {
	mode     uint32
	chown    bool
	uid, gid int
}

func modeChange(arg uint64) attrChange { return attrChange{mode: uint32(arg)} }

func (c *call) ownerChange(first int) attrChange {
	return attrChange{chown: true, uid: int(int32(uint32(c.args[first]))), gid: int(int32(uint32(c.args[first+1])))}
}

// chmodFD performs fchmod and fchown, and chmod through a descriptor's /proc path, on
// descriptor n of the caller. The supervisor opens the descriptor's inode
// itself and checks and changes that inode, so swapping the descriptor during
// the call changes nothing but which unprotected file is changed.
func (c *call) chmodFD(n int, mode attrChange, viaPath bool) reply {
	if !viaPath && c.fdFlags(n)&unix.O_PATH != 0 {
		// fchmod refuses an O_PATH descriptor; chmod on its /proc path
		// does not.
		return fail(unix.EBADF)
	}
	fd, err := c.peerFD(n)
	if err != nil {
		return fail(unix.EBADF)
	}
	defer unix.Close(fd)
	return c.chmodOpened(fd, mode)
}

// fdFlags returns the open flags of the caller's descriptor n.
func (c *call) fdFlags(n int) int {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(c.tid) + "/fdinfo/" + strconv.Itoa(n))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "flags:"); ok {
			flags, _ := strconv.ParseInt(strings.TrimSpace(value), 8, 64)
			return int(flags)
		}
	}
	return 0
}
