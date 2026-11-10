package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/gofrs/flock"
)

var ErrNotFound = errors.New("session not found")
var ErrClosed = errors.New("session store is closed")

type Meta struct {
	CreatedAt time.Time `json:"-"`
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Model     string    `json:"model"`
	Provider  string    `json:"provider"`
	CWD       string    `json:"cwd"`
	Goal      string    `json:"goal"`
	// GoalMemoryOff records that the goal text was written while memory was off.
	GoalMemoryOff   bool      `json:"goal_memory_off,omitempty"`
	ForkedFrom      string    `json:"forked_from"`
	ForkSeq         int       `json:"fork_seq"`
	Tags            []string  `json:"tags"`
	Pinned          bool      `json:"pinned"`
	Archived        bool      `json:"archived"`
	Shared          bool      `json:"shared"`
	ShareCreatedAt  time.Time `json:"share_created_at,omitempty"`
	ShareUpdatedAt  time.Time `json:"share_updated_at,omitempty"`
	ShareToken      string    `json:"share_token,omitempty"`
	ShareRedactTool bool      `json:"share_redact_tool,omitempty"`
	Effort          string    `json:"effort"`
	UsageIn         int       `json:"usage_in"`
	UsageCached     int       `json:"usage_cached"`
	UsageOut        int       `json:"usage_out"`
	UsageCacheWrite int       `json:"usage_cache_write,omitempty"`

	SubUsage   map[string]ai.Usage `json:"sub_usage"`
	ModelUsage map[string]ai.Usage `json:"model_usage,omitempty"`
	UpdatedAt  time.Time           `json:"updated_at"`

	TaskID                   string `json:"task_id"`
	ImportSourceID           string `json:"import_source_id,omitempty"`
	ImportFingerprint        string `json:"import_fingerprint,omitempty"`
	ImportMetadata           string `json:"import_metadata,omitempty"`
	ImportContentFingerprint string `json:"import_content_fingerprint,omitempty"`
}

type Task struct {
	Model       string    `json:"model,omitempty"`
	Provider    string    `json:"provider,omitempty"`
	ID          string    `json:"id"`
	Description string    `json:"description"`
	Prompt      string    `json:"prompt"`
	Status      string    `json:"status"`
	Report      string    `json:"report"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at"`
	// ParentToolCallID is the parent's subagent tool call that started it.
	ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
}

type Schedule struct {
	ID       int       `json:"id"`
	Schedule string    `json:"schedule"`
	Prompt   string    `json:"prompt"`
	Anchor   time.Time `json:"anchor"`
	LastFire time.Time `json:"last_fire"`
}

type Compaction struct {
	RunID     string   `json:"run_id,omitempty"`
	Seq       int      `json:"seq"`
	Cutoff    int      `json:"cutoff"`
	Summary   string   `json:"summary"`
	Model     string   `json:"model"`
	Usage     ai.Usage `json:"usage"`
	DropPrior bool     `json:"drop_prior,omitempty"`
	Fresh     bool     `json:"fresh,omitempty"`
}

type Store struct {
	filesDir             string
	mu                   sync.Mutex
	lock                 *flock.Flock
	closed               bool
	projectScoped        bool
	summaries            *summaryState
	summaryScanInterval  time.Duration
	summaryBeaconPoll    time.Duration
	summaryRunProbe      time.Duration
	summaryScanLease     time.Duration
	summaryCoalesce      time.Duration
	summarySnapshotLease time.Duration
	readObserver         func(string)
	summaryWaitObserver  func()
	// runLeases 是本进程持有的运行租约（见 run_lease.go）。
	runLeases map[string]*heldRun
}

type sessionData struct {
	Meta        Meta
	CreatedAt   time.Time
	Todos       string
	Messages    map[int]ai.Message
	Tasks       map[string]Task
	Snapshots   map[int]string
	Schedules   map[int]Schedule
	Compactions map[int]Compaction
}

func newData(meta Meta) *sessionData {
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = meta.UpdatedAt
	}
	return &sessionData{Meta: meta, CreatedAt: meta.CreatedAt,
		Messages: map[int]ai.Message{}, Tasks: map[string]Task{}, Snapshots: map[int]string{},
		Schedules: map[int]Schedule{}, Compactions: map[int]Compaction{}}
}

func OpenHome(home string) (*Store, error) { return Open(filepath.Join(home, "sessions")) }

func OpenProjectHome(home string) (*Store, error) {
	return OpenProjectDir(filepath.Join(home, "sessions"))
}

// OpenProjectDir reads both flat legacy sessions and project-grouped sessions.
func OpenProjectDir(dir string) (*Store, error) {
	s, err := Open(dir)
	if err != nil {
		return nil, err
	}
	s.projectScoped = true
	return s, nil
}

func prepareLockFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if createErr != nil {
			return createErr
		}
		return file.Close()
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("lock path is not a regular file")
	}
	return os.Chmod(path, 0600)
}

func Open(dir string) (*Store, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if err := prepareLockFile(filepath.Join(root, ".lock")); err != nil {
		return nil, fmt.Errorf("prepare session lock: %w", err)
	}
	s := &Store{
		filesDir:             root,
		lock:                 flock.New(filepath.Join(root, ".lock")),
		summaryScanInterval:  30 * time.Second,
		summaryBeaconPoll:    500 * time.Millisecond,
		summaryRunProbe:      2 * time.Second,
		summaryScanLease:     90 * time.Second,
		summaryCoalesce:      500 * time.Millisecond,
		summarySnapshotLease: 60 * time.Second,
	}
	if err := s.withLock(func() error { return nil }); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) SessionsDir() string { return s.filesDir }

func validID(id string) bool {
	if id == "" {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (s *Store) TranscriptPath(id string) string {
	if !validID(id) {
		return ""
	}
	direct := filepath.Join(s.filesDir, id, "session.jsonl")
	if !s.projectScoped {
		return direct
	}
	if path, err := FindTranscript(s.filesDir, id); err == nil {
		return path
	} else if !errors.Is(err, ErrNotFound) && !errors.Is(err, os.ErrNotExist) {
		return ""
	}
	return direct
}

// CompactionDir is the session directory's "compaction" subdirectory, where the agent
// saves compacted-away turns as markdown. It moves and is deleted with the session.
func (s *Store) CompactionDir(id string) string {
	path := s.TranscriptPath(id)
	if path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(path), "compaction")
}

func (s *Store) withLock(fn func() error) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	locked, err := s.lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return fmt.Errorf("lock sessions: %w", err)
	}
	if !locked {
		return fmt.Errorf("lock sessions: %w", ctx.Err())
	}
	defer func() { err = errors.Join(err, s.lock.Unlock()) }()
	return fn()
}

func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	var scannerDone chan struct{}
	if s.summaries != nil {
		if s.summaries.publishTimer != nil {
			s.summaries.publishTimer.Stop()
			s.summaries.publishTimer = nil
		}
		if s.summaries.journalTimer != nil {
			s.summaries.journalTimer.Stop()
			s.summaries.journalTimer = nil
		}
		if s.summaries.scannerStop != nil {
			close(s.summaries.scannerStop)
			s.summaries.scannerStop = nil
		}
		scannerDone = s.summaries.scannerDone
		close(s.summaries.notify)
		s.summaries.notify = make(chan struct{})
	}
	s.mu.Unlock()
	if scannerDone != nil {
		<-scannerDone
	}
	return s.lock.Close()
}

func (s *Store) get(id string) (d *sessionData, err error) {
	err = s.withLock(func() error { var e error; d, e = s.read(id); return e })
	return
}

func (s *Store) update(id string, fn func(*sessionData) error) error {
	return s.withLock(func() error {
		d, err := s.read(id)
		if err != nil {
			return err
		}
		if err := fn(d); err != nil {
			return err
		}
		return s.write(d)
	})
}

// updateMessages 是修改会话消息的 update：另一个进程正在这个会话里运行回合时拒绝，
// 以免覆盖对方即将写入的消息。只改元数据（标题、置顶、归档等）的写入不受影响。
func (s *Store) updateMessages(id string, fn func(*sessionData) error) error {
	return s.update(id, func(d *sessionData) error {
		if err := s.guardExternalRunLocked(d.Meta.ID); err != nil {
			return err
		}
		return fn(d)
	})
}

func (s *Store) ids() ([]string, error) {
	entries, err := os.ReadDir(s.filesDir)
	if err != nil {
		return nil, err
	}
	var ids []string
	seen := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidates := []string{e.Name()}
		if s.projectScoped && validProjectDir(e.Name()) {
			sub, subErr := os.ReadDir(filepath.Join(s.filesDir, e.Name()))
			if subErr != nil {
				return nil, subErr
			}
			for _, child := range sub {
				if child.IsDir() && validID(child.Name()) {
					candidates = append(candidates, child.Name())
				}
			}
		}
		for _, id := range candidates {
			if !validID(id) || seen[id] {
				continue
			}
			info, err := os.Lstat(s.TranscriptPath(id))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("invalid session file for %s", id)
			}
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (s *Store) all() ([]*sessionData, error) {
	ids, err := s.ids()
	if err != nil {
		return nil, err
	}
	var out []*sessionData
	for _, id := range ids {
		d, err := s.read(id)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		return metaBefore(out[i].Meta, out[j].Meta)
	})
	return out, nil
}

func (s *Store) create(d *sessionData) (string, error) {
	for {
		b := make([]byte, 4)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		d.Meta.ID = hex.EncodeToString(b)
		dir := filepath.Join(s.filesDir, d.Meta.ID)
		if s.projectScoped {
			dir = filepath.Join(s.filesDir, projectDir(d.Meta.CWD), d.Meta.ID)
		}
		if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
			return "", err
		}
		if err := os.Mkdir(dir, 0700); errors.Is(err, os.ErrExist) {
			continue
		} else if err != nil {
			return "", err
		}
		if err := s.write(d); err != nil {
			_ = os.Remove(dir)
			return "", err
		}
		return d.Meta.ID, nil
	}
}

func validProjectDir(name string) bool {
	return len(name) > 8 && name != ".lock" && name != ".deleted-history"
}

func projectDir(cwd string) string {
	base := filepath.Base(filepath.Clean(cwd))
	if base == "." || base == string(filepath.Separator) || base == "" {
		base = "workspace"
	}
	var b strings.Builder
	for _, r := range base {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	sum := sha256.Sum256([]byte(filepath.Clean(cwd)))
	return fmt.Sprintf("%s-%x", strings.Trim(b.String(), "-"), sum[:4])
}

func (s *Store) Create(cwd, model, provider string) (id string, err error) {
	err = s.withLock(func() error {
		var e error
		id, e = s.create(newData(Meta{CWD: cwd, Model: model, Provider: provider, UpdatedAt: time.Now().UTC()}))
		return e
	})
	return
}

func sortedKeys[V any](m map[int]V) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}
