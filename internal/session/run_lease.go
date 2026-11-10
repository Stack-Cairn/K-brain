package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// 运行租约：同一个会话同一时刻只允许一个进程运行回合（kn、backend 或另一个 kn）。
// 租约是 sessions/.runs/<id>.lock 上的操作系统文件锁，进程退出或崩溃时由系统释放，
// 不会留下过期标记；持有者另写 <id>.json 说明自己是谁，供其他进程展示。
const (
	runLeaseDir  = ".runs"
	syncBeacon   = ".sync"
	runLockWait  = 300 * time.Millisecond
	runLockRetry = 20 * time.Millisecond
)

// ErrRunElsewhere 表示另一个进程正在这个会话里运行回合。
var ErrRunElsewhere = errors.New("session is running in another process")

// ErrHistoryStale 表示调用方持有的 History 落后于磁盘上的记录（另一个进程写入了消息）；
// 保存会覆盖对方的消息，所以被拒绝。调用方应重新加载会话。
var ErrHistoryStale = errors.New("session history changed in another process")

// ExternalRun 描述另一个进程中正在运行的回合。
type ExternalRun struct {
	Client    string    `json:"client"`
	PID       int       `json:"pid,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

// RunElsewhereError 带上占用会话的进程信息；errors.Is(err, ErrRunElsewhere) 成立。
type RunElsewhereError struct{ Run ExternalRun }

func (e *RunElsewhereError) Error() string {
	if e.Run.Client == "" {
		return ErrRunElsewhere.Error()
	}
	return fmt.Sprintf("session is running in %s", e.Run.Client)
}

func (e *RunElsewhereError) Is(target error) bool { return target == ErrRunElsewhere }

// RunLease 是本进程持有的运行租约。
type RunLease struct {
	store *Store
	id    string
	lock  *flock.Flock
}

func (s *Store) runLeasePaths(id string) (lockPath, ownerPath string) {
	dir := filepath.Join(s.filesDir, runLeaseDir)
	return filepath.Join(dir, id+".lock"), filepath.Join(dir, id+".json")
}

func readExternalRun(ownerPath string) ExternalRun {
	var run ExternalRun
	data, err := os.ReadFile(ownerPath)
	if err == nil {
		_ = json.Unmarshal(data, &run)
	}
	if run.Client == "" {
		run.Client = "another process"
	}
	return run
}

// AcquireRun 为会话 id 取得运行租约。另一个进程持有时返回 *RunElsewhereError；
// 本进程已持有时同样返回该错误（Client 为本进程的 client）。
func (s *Store) AcquireRun(id, client string) (*RunLease, error) {
	if !validID(id) {
		return nil, ErrNotFound
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	if held := s.runLeases[id]; held != nil {
		s.mu.Unlock()
		return nil, &RunElsewhereError{Run: held.owner}
	}
	s.mu.Unlock()
	lockPath, ownerPath := s.runLeasePaths(id)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, err
	}
	lock := flock.New(lockPath)
	ctx, cancel := context.WithTimeout(context.Background(), runLockWait)
	defer cancel()
	locked, err := lock.TryLockContext(ctx, runLockRetry)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("lock session run: %w", err)
	}
	if !locked {
		_ = lock.Close()
		return nil, &RunElsewhereError{Run: readExternalRun(ownerPath)}
	}
	owner := ExternalRun{Client: client, PID: os.Getpid(), StartedAt: time.Now().UTC()}
	data, _ := json.Marshal(owner)
	if err := os.WriteFile(ownerPath, data, 0o600); err != nil {
		_ = lock.Unlock()
		return nil, err
	}
	lease := &RunLease{store: s, id: id, lock: lock}
	s.mu.Lock()
	if s.runLeases == nil {
		s.runLeases = map[string]*heldRun{}
	}
	s.runLeases[id] = &heldRun{lease: lease, owner: owner}
	s.mu.Unlock()
	s.bumpSyncBeacon()
	return lease, nil
}

type heldRun struct {
	lease *RunLease
	owner ExternalRun
}

// Release 释放租约；可以重复调用。
func (l *RunLease) Release() error {
	if l == nil {
		return nil
	}
	s := l.store
	s.mu.Lock()
	held := s.runLeases[l.id]
	if held == nil || held.lease != l {
		s.mu.Unlock()
		return nil
	}
	delete(s.runLeases, l.id)
	s.mu.Unlock()
	_, ownerPath := s.runLeasePaths(l.id)
	// 先删说明文件再解锁：看到锁被占用的探测方总能读到持有者。
	_ = os.Remove(ownerPath)
	err := l.lock.Unlock()
	s.bumpSyncBeacon()
	return err
}

// ID 返回租约所属的会话。
func (l *RunLease) ID() string { return l.id }

// ExternalRun 报告另一个进程是否正在会话 id 里运行回合。本进程持有的租约不算。
func (s *Store) ExternalRun(id string) (ExternalRun, bool) {
	if !validID(id) {
		return ExternalRun{}, false
	}
	s.mu.Lock()
	_, own := s.runLeases[id]
	s.mu.Unlock()
	if own {
		return ExternalRun{}, false
	}
	return s.probeRun(id)
}

func (s *Store) probeRun(id string) (ExternalRun, bool) {
	lockPath, ownerPath := s.runLeasePaths(id)
	if _, err := os.Lstat(lockPath); err != nil {
		return ExternalRun{}, false
	}
	probe := flock.New(lockPath)
	defer probe.Close()
	free, err := probe.TryRLock()
	if err != nil {
		return ExternalRun{}, false
	}
	if free {
		_ = probe.Unlock()
		return ExternalRun{}, false
	}
	return readExternalRun(ownerPath), true
}

// externalRunsLocked 探测 .runs 目录里所有不属于本进程的租约。调用方持有 s.mu。
func (s *Store) externalRunsLocked() map[string]ExternalRun {
	entries, err := os.ReadDir(filepath.Join(s.filesDir, runLeaseDir))
	if err != nil {
		return nil
	}
	var out map[string]ExternalRun
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".lock")
		if !ok || !validID(id) || s.runLeases[id] != nil {
			continue
		}
		if run, busy := s.probeRun(id); busy {
			if out == nil {
				out = map[string]ExternalRun{}
			}
			out[id] = run
		}
	}
	return out
}

// removeRunLeaseFilesLocked 在删除会话后清理它的租约文件；本进程持有时保留。
func (s *Store) removeRunLeaseFilesLocked(id string) {
	if s.runLeases[id] != nil {
		return
	}
	lockPath, ownerPath := s.runLeasePaths(id)
	_ = os.Remove(ownerPath)
	_ = os.Remove(lockPath)
}

// guardExternalRunLocked 拒绝在另一个进程运行回合时修改会话消息。
func (s *Store) guardExternalRunLocked(id string) error {
	if s.runLeases[id] != nil {
		return nil
	}
	if run, busy := s.probeRun(id); busy {
		return &RunElsewhereError{Run: run}
	}
	return nil
}

// bumpSyncBeacon 在会话目录写入变化计数，让其他进程（backend 的会话列表）
// 只需 stat 一个文件就能及时发现本进程写入的会话变化。失败只影响发现速度。
func (s *Store) bumpSyncBeacon() {
	path := filepath.Join(s.filesDir, syncBeacon)
	next := uint64(1)
	if data, err := os.ReadFile(path); err == nil {
		if n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); err == nil {
			next = n + 1
		}
	}
	_ = os.WriteFile(path, []byte(strconv.FormatUint(next, 10)), 0o600)
}

type beaconStamp struct {
	size    int64
	modTime time.Time
	ok      bool
}

func (b beaconStamp) equal(other beaconStamp) bool {
	return b.ok == other.ok && b.size == other.size && b.modTime.Equal(other.modTime)
}

func (s *Store) syncBeaconStamp() beaconStamp {
	info, err := os.Stat(filepath.Join(s.filesDir, syncBeacon))
	if err != nil {
		return beaconStamp{}
	}
	return beaconStamp{size: info.Size(), modTime: info.ModTime(), ok: true}
}
