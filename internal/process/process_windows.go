package process

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func Configure(cmd *exec.Cmd, _ bool) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
	cmd.Cancel = func() error { return Kill(cmd) }
}

func Kill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	var killErr error
	err := cmd.Process.WithHandle(func(handle uintptr) {
		killErr = killTree(cmd, windows.Handle(handle))
	})
	if err != nil {
		return os.ErrProcessDone
	}
	return killErr
}

func Alive(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	alive := false
	_ = cmd.Process.WithHandle(func(handle uintptr) {
		state, err := windows.WaitForSingleObject(windows.Handle(handle), 0)
		alive = err == nil && state == uint32(windows.WAIT_TIMEOUT)
	})
	return alive
}

type treeProcess struct {
	pid     uint32
	handle  windows.Handle
	created uint64
}

func processCreated(handle windows.Handle) (uint64, error) {
	var created, exited, kernel, user windows.Filetime
	err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user)
	return uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime), err
}

func processParent(handle windows.Handle) (uint32, error) {
	var info windows.PROCESS_BASIC_INFORMATION
	err := windows.NtQueryInformationProcess(handle, windows.ProcessBasicInformation, unsafe.Pointer(&info), uint32(unsafe.Sizeof(info)), nil)
	return uint32(info.InheritedFromUniqueProcessId), err
}

func processChildren() (map[uint32][]uint32, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	children := make(map[uint32][]uint32)
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return children, nil
}

func killTree(cmd *exec.Cmd, root windows.Handle) error {
	created, err := processCreated(root)
	if err != nil {
		return errors.Join(err, cmd.Process.Kill())
	}
	children, err := processChildren()
	if err != nil {
		return errors.Join(err, cmd.Process.Kill())
	}
	processes := []treeProcess{{uint32(cmd.Process.Pid), root, created}}
	defer func() {
		for _, p := range processes[1:] {
			_ = windows.CloseHandle(p.handle)
		}
	}()
	seen := map[uint32]bool{uint32(cmd.Process.Pid): true}
	var errs []error
	for i := 0; i < len(processes); i++ {
		parent := processes[i]
		for _, pid := range children[parent.pid] {
			if seen[pid] {
				continue
			}
			seen[pid] = true
			h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
			if err != nil {
				if !errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
					errs = append(errs, fmt.Errorf("open child %d: %w", pid, err))
				}
				continue
			}
			created, err := processCreated(h)
			ppid, parentErr := processParent(h)
			err = errors.Join(err, parentErr)
			if err != nil || created < parent.created || ppid != parent.pid {
				_ = windows.CloseHandle(h)
				if err != nil {
					errs = append(errs, fmt.Errorf("inspect child %d: %w", pid, err))
				}
				continue
			}
			processes = append(processes, treeProcess{pid, h, created})
		}
	}
	for i := len(processes) - 1; i > 0; i-- {
		p := processes[i]
		if err := windows.TerminateProcess(p.handle, 1); err != nil {
			if state, waitErr := windows.WaitForSingleObject(p.handle, 0); waitErr != nil || state != windows.WAIT_OBJECT_0 {
				errs = append(errs, fmt.Errorf("terminate child %d: %w", p.pid, err))
			}
		}
	}
	errs = append(errs, cmd.Process.Kill())
	deadline := time.Now().Add(time.Second)
	for _, p := range processes[1:] {
		remaining := max(0, time.Until(deadline).Milliseconds())
		state, err := windows.WaitForSingleObject(p.handle, uint32(remaining))
		if err != nil {
			errs = append(errs, fmt.Errorf("wait for child %d: %w", p.pid, err))
		} else if state == uint32(windows.WAIT_TIMEOUT) {
			errs = append(errs, fmt.Errorf("child %d did not exit after termination", p.pid))
		}
	}
	return errors.Join(errs...)
}
