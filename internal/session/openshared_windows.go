package session

import (
	"os"
	"syscall"
)

// openShared 以只读方式打开文件，并允许其他句柄同时改名或替换它。os.Open 不带
// FILE_SHARE_DELETE，不持有会话锁的读者（用量统计）打开会话文件时，正在运行的回合
// 保存会话的 os.Rename 会失败（Access is denied）。做法同 permissions/readfile_windows.go。
func openShared(path string) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}
