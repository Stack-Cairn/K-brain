package session

import (
	"os"
	"syscall"
)

// openShared 以只读方式打开文件，共享读、写和删除，做法同 permissions/readfile_windows.go。
// 这只让其他句柄能删除这个文件；用 MoveFileEx 替换它仍要等这个句柄关闭，所以调用方应当
// 读完就关（见 readSessionFile），保存那边用 renameRetry 重试。
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
