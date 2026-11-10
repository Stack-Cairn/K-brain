package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
)

// 子代理租约：后台子代理会在回合结束后继续运行，运行租约（run_lease.go）覆盖不到。
// 进程开始为某个会话运行子代理时取得 sessions/.runs/tasks/<会话>.<pid>-<随机>.lock 上的
// 文件锁，并把锁名记在它保存的 Task.Lease 里；其他进程探测这把锁就知道一条状态为
// running 的记录是真的还在运行，还是运行它的进程已经退出（锁由系统释放）。
// 锁名带随机部分，进程号被复用也不会让旧记录看起来还活着。
const taskLeaseDir = "tasks"

type taskLease struct {
	token string
	path  string
	lock  *flock.Flock
}

// release 先解锁再删除（Windows 上打开的文件删不掉）；中间探测到的空闲锁同样表示不在运行。
func (l *taskLease) release() {
	_ = l.lock.Close()
	_ = os.Remove(l.path)
}

func (s *Store) taskLeasePath(token string) string {
	return filepath.Join(s.filesDir, runLeaseDir, taskLeaseDir, token+".lock")
}

func validTaskLease(token string) bool {
	return token != "" && !strings.HasPrefix(token, ".") && !strings.ContainsAny(token, `/\`) && filepath.Base(token) == token
}

// TaskLease 返回本进程在会话 id 里运行子代理用的租约名，第一次调用时取得租约。
// 租约一直持有到 Close（进程退出时由系统释放）。
func (s *Store) TaskLease(id string) (string, error) {
	if !validID(id) {
		return "", ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", ErrClosed
	}
	if held := s.taskLeases[id]; held != nil {
		return held.token, nil
	}
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := fmt.Sprintf("%s.%d-%s", id, os.Getpid(), hex.EncodeToString(b))
	path := s.taskLeasePath(token)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	lock := flock.New(path)
	locked, err := lock.TryLock()
	if err != nil {
		return "", fmt.Errorf("lock subagent lease: %w", err)
	}
	if !locked {
		_ = lock.Close()
		return "", fmt.Errorf("lock subagent lease: %s is held", token)
	}
	if s.taskLeases == nil {
		s.taskLeases = map[string]*taskLease{}
	}
	s.taskLeases[id] = &taskLease{token: token, path: path, lock: lock}
	return token, nil
}

// TaskLeaseLive 报告持有子代理租约 token 的进程是否还在运行；本进程持有的也算。
func (s *Store) TaskLeaseLive(token string) bool {
	if !validTaskLease(token) {
		return false
	}
	s.mu.Lock()
	for _, held := range s.taskLeases {
		if held.token == token {
			s.mu.Unlock()
			return true
		}
	}
	s.mu.Unlock()
	path := s.taskLeasePath(token)
	if _, err := os.Lstat(path); err != nil {
		return false
	}
	probe := flock.New(path)
	defer probe.Close()
	free, err := probe.TryRLock()
	if err != nil {
		return false
	}
	if free {
		_ = probe.Unlock()
		return false
	}
	return true
}

// TaskRunningElsewhere 报告一条保存的子代理记录是否正在另一个仍在运行的进程里运行。
func (s *Store) TaskRunningElsewhere(t Task) bool {
	if t.Status != "running" || t.Lease == "" {
		return false
	}
	s.mu.Lock()
	for _, held := range s.taskLeases {
		if held.token == t.Lease {
			s.mu.Unlock()
			return false
		}
	}
	s.mu.Unlock()
	return s.TaskLeaseLive(t.Lease)
}
