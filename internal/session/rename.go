package session

import (
	"os"
	"runtime"
	"time"
)

// renameRetry 把 from 改名为 to（替换已有的会话文件或移动会话目录）。Windows 上只要另一个句柄
// 还开着目标文件（或目录里的文件），改名就会失败（Access is denied），即使对方带了
// FILE_SHARE_DELETE；用量统计读会话文件的那一瞬间、杀毒软件和索引服务打开新写的文件都会
// 这样，所以在 Windows 上重试约四秒。做法同 permissions/store.go 的 renameRetry。
func renameRetry(from, to string) error {
	err := os.Rename(from, to)
	for attempt := 1; err != nil && runtime.GOOS == "windows" && attempt <= 40; attempt++ {
		time.Sleep(time.Duration(min(attempt, 10)) * 10 * time.Millisecond)
		err = os.Rename(from, to)
	}
	return err
}
