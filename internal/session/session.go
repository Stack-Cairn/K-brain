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
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Model           string   `json:"model"`
	Provider        string   `json:"provider"`
	CWD             string   `json:"cwd"`
	Goal            string   `json:"goal"`
	ForkedFrom      string   `json:"forked_from"`
	ForkSeq         int      `json:"fork_seq"`
	Tags            []string `json:"tags"`
	Pinned          bool     `json:"pinned"`
	Archived        bool     `json:"archived"`
	Effort          string   `json:"effort"`
	UsageIn         int      `json:"usage_in"`
	UsageCached     int      `json:"usage_cached"`
	UsageOut        int      `json:"usage_out"`
	UsageCacheWrite int      `json:"usage_cache_write,omitempty"`

	SubUsage   map[string]ai.Usage `json:"sub_usage"`
	ModelUsage map[string]ai.Usage `json:"model_usage,omitempty"`
	UpdatedAt  time.Time           `json:"updated_at"`

	TaskID string `json:"task_id"`
}

type Task struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	Prompt      string    `json:"prompt"`
	Status      string    `json:"status"`
	Report      string    `json:"report"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at"`
}

type Schedule struct {
	ID       int       `json:"id"`
	Schedule string    `json:"schedule"`
	Prompt   string    `json:"prompt"`
	Anchor   time.Time `json:"anchor"`
	LastFire time.Time `json:"last_fire"`
}

type Compaction struct {
	Seq       int      `json:"seq"`
	Cutoff    int      `json:"cutoff"`
	Summary   string   `json:"summary"`
	Model     string   `json:"model"`
	Usage     ai.Usage `json:"usage"`
	DropPrior bool     `json:"drop_prior,omitempty"`
}

type Store struct {
	filesDir      string
	mu            sync.Mutex
	lock          *flock.Flock
	closed        bool
	projectScoped bool
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
	return &sessionData{Meta: meta, CreatedAt: meta.UpdatedAt,
		Messages: map[int]ai.Message{}, Tasks: map[string]Task{}, Snapshots: map[int]string{},
		Schedules: map[int]Schedule{}, Compactions: map[int]Compaction{}}
}

func OpenHome(home string) (*Store, error) { return Open(filepath.Join(home, "sessions")) }

func OpenProjectHome(home string) (*Store, error) {
	s, err := Open(filepath.Join(home, "sessions"))
	if err != nil {
		return nil, err
	}
	s.projectScoped = true
	return s, nil
}

func Open(dir string) (*Store, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	s := &Store{filesDir: root, lock: flock.New(filepath.Join(root, ".lock"))}
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
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
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
	return len(name) > 8 && name != ".lock"
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
