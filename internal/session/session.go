package session

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
	id         TEXT PRIMARY KEY,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	cwd        TEXT NOT NULL,
	model      TEXT NOT NULL,
	provider   TEXT NOT NULL,
	title      TEXT NOT NULL DEFAULT '',
	goal       TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS messages (
	session_id TEXT NOT NULL REFERENCES sessions(id),
	seq        INTEGER NOT NULL,
	role       TEXT NOT NULL,
	content    TEXT NOT NULL, -- ai.Message JSON
	PRIMARY KEY (session_id, seq)
);
CREATE TABLE IF NOT EXISTS tasks (
	session_id  TEXT NOT NULL REFERENCES sessions(id),
	task_id     TEXT NOT NULL,
	description TEXT NOT NULL,
	prompt      TEXT NOT NULL,
	status      TEXT NOT NULL,
	report      TEXT NOT NULL DEFAULT '',
	started_at  TEXT NOT NULL,
	ended_at    TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (session_id, task_id)
);
-- Workspace snapshots: one git stash ref per turn (keyed by the conversation
-- index the turn started at), so a conversation rewind can also restore the
-- files that turn changed. Same seq semantics as messages, so DeleteFrom
-- trims both together.
CREATE TABLE IF NOT EXISTS snapshots (
	session_id TEXT NOT NULL REFERENCES sessions(id),
	seq        INTEGER NOT NULL,
	ref        TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (session_id, seq)
);
-- Scheduled tasks: the wakeup channel's durable records. One row per task,
-- keyed by the session it fires into. The store keeps the schedule
-- expression, anchor, and last fire; the TUI's ticker evaluates due tasks.
CREATE TABLE IF NOT EXISTS schedules (
	session_id TEXT NOT NULL REFERENCES sessions(id),
	id         INTEGER NOT NULL,
	schedule   TEXT NOT NULL,      -- '@every 10m' | '@at <rfc3339>'
	prompt     TEXT NOT NULL,      -- the machine-authored turn to submit on fire
	anchor     TEXT NOT NULL,      -- grid origin (RFC3339)
	last_fire  TEXT NOT NULL DEFAULT '', -- last fire time ("" = never); one-shots complete here
	created_at TEXT NOT NULL,
	PRIMARY KEY (session_id, id)
);
-- Compaction events: append-only. Each row records a compaction as summary +
-- cutoff (the raw-log seq it folded). The messages table is never rewritten
-- by a compaction — Load derives the compacted view from the latest event,
-- so a bad compaction is inspectable and retryable.
CREATE TABLE IF NOT EXISTS compactions (
	session_id TEXT NOT NULL REFERENCES sessions(id),
	seq        INTEGER NOT NULL, -- compaction generation, 1-based
	cutoff     INTEGER NOT NULL, -- raw-log seq the summary replaces (1..cutoff-1)
	summary    TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (session_id, seq)
);`

var extraColumns = []struct{ name, def string }{
	{"forked_from", "forked_from TEXT NOT NULL DEFAULT ''"},
	{"fork_seq", "fork_seq INTEGER NOT NULL DEFAULT 0"},
	{"tags", "tags TEXT NOT NULL DEFAULT ''"},
	{"pinned", "pinned INTEGER NOT NULL DEFAULT 0"},
	{"effort", "effort TEXT NOT NULL DEFAULT ''"},
	{"usage_in", "usage_in INTEGER NOT NULL DEFAULT 0"},
	{"usage_cached", "usage_cached INTEGER NOT NULL DEFAULT 0"},
	{"usage_out", "usage_out INTEGER NOT NULL DEFAULT 0"},
	{"todos", "todos TEXT NOT NULL DEFAULT ''"},
	{"task_id", "task_id TEXT NOT NULL DEFAULT ''"},
	{"sub_usage", "sub_usage TEXT NOT NULL DEFAULT ''"},
}

var compactionColumns = []struct{ name, def string }{
	{"model", "model TEXT NOT NULL DEFAULT ''"},
	{"usage", "usage TEXT NOT NULL DEFAULT ''"},
}

type Meta struct {
	ID          string
	Title       string
	Model       string
	Provider    string
	CWD         string
	Goal        string
	ForkedFrom  string
	ForkSeq     int
	Tags        []string
	Pinned      bool
	Effort      string
	UsageIn     int
	UsageCached int
	UsageOut    int

	SubUsage  map[string]ai.Usage
	UpdatedAt time.Time

	TaskID string
}

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA temp_store=MEMORY",
	} {
		if _, err := db.ExecContext(context.Background(), pragma); err != nil {
			return nil, err
		}
	}
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		return nil, err
	}

	_, _ = db.ExecContext(context.Background(), `ALTER TABLE sessions ADD COLUMN goal TEXT NOT NULL DEFAULT ''`)

	for _, c := range extraColumns {
		_, _ = db.ExecContext(context.Background(), `ALTER TABLE sessions ADD COLUMN `+c.def)
	}
	for _, c := range compactionColumns {
		_, _ = db.ExecContext(context.Background(), `ALTER TABLE compactions ADD COLUMN `+c.def)
	}
	return &Store{db: db}, nil
}

func (s *Store) SetGoal(id, goal string) error {
	_, err := s.db.ExecContext(context.Background(), `UPDATE sessions SET goal=? WHERE id=?`, goal, id)
	return err
}

func (s *Store) SetTodos(id, todosJSON string) error {
	_, err := s.db.ExecContext(context.Background(), `UPDATE sessions SET todos=? WHERE id=?`, todosJSON, id)
	return err
}

func (s *Store) Todos(id string) string {
	var v string
	_ = s.db.QueryRowContext(context.Background(), `SELECT todos FROM sessions WHERE id=?`, id).Scan(&v)
	return v
}

func (s *Store) SetEffort(id, effort string) error {
	_, err := s.db.ExecContext(context.Background(), `UPDATE sessions SET effort=? WHERE id=?`, effort, id)
	return err
}

func (s *Store) SetUsage(id string, in, cached, out int, sub map[string]ai.Usage) error {
	subJSON := ""
	if len(sub) > 0 {
		b, err := json.Marshal(sub)
		if err != nil {
			return err
		}
		subJSON = string(b)
	}
	_, err := s.db.ExecContext(context.Background(), `UPDATE sessions SET usage_in=?, usage_cached=?, usage_out=?, sub_usage=? WHERE id=?`, in, cached, out, subJSON, id)
	return err
}

type Task struct {
	ID          string
	Description string
	Prompt      string
	Status      string
	Report      string
	StartedAt   time.Time
	EndedAt     time.Time
}

func (s *Store) SaveTask(sessionID string, t Task) error {
	ended := ""
	if !t.EndedAt.IsZero() {
		ended = t.EndedAt.UTC().Format(time.RFC3339)
	}
	_, err := s.db.ExecContext(context.Background(), `INSERT OR REPLACE INTO tasks
		(session_id, task_id, description, prompt, status, report, started_at, ended_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		sessionID, t.ID, t.Description, t.Prompt, t.Status, t.Report,
		t.StartedAt.UTC().Format(time.RFC3339), ended)
	return err
}

func (s *Store) LoadTasks(sessionID string) ([]Task, error) {
	rows, err := s.db.QueryContext(context.Background(), `SELECT task_id, description, prompt, status, report, started_at, ended_at
		FROM tasks WHERE session_id=? ORDER BY started_at`, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Task
	for rows.Next() {
		var t Task
		var started, ended string
		if err := rows.Scan(&t.ID, &t.Description, &t.Prompt, &t.Status, &t.Report, &started, &ended); err != nil {
			return nil, err
		}
		t.StartedAt, _ = time.Parse(time.RFC3339, started)
		if ended != "" {
			t.EndedAt, _ = time.Parse(time.RFC3339, ended)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) Close() error { return s.db.Close() }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func (s *Store) SaveSubagentTranscript(parentID, taskID string, msgs []ai.Message, model, provider string) (string, error) {
	if parentID == "" || taskID == "" {
		return "", nil
	}
	id := subagentSessionID(parentID, taskID)
	if _, err := s.db.ExecContext(context.Background(), `INSERT INTO sessions
		(id, created_at, updated_at, cwd, model, provider, title, forked_from, task_id)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET updated_at=excluded.updated_at, model=excluded.model, provider=excluded.provider`,
		id, now(), now(), "", model, provider, "subagent "+taskID, parentID, taskID); err != nil {
		return "", err
	}
	if err := s.Save(id, 0, msgs, model, provider); err != nil {
		return "", err
	}

	if _, err := s.db.ExecContext(context.Background(), `DELETE FROM messages WHERE session_id=? AND seq>=?`, id, len(msgs)); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) SubagentTranscript(parentID, taskID string) ([]ai.Message, error) {
	if parentID == "" || taskID == "" {
		return nil, nil
	}
	return s.loadMessages(subagentSessionID(parentID, taskID))
}

func subagentSessionID(parentID, taskID string) string {
	return "task-" + parentID + "-" + taskID
}

func (s *Store) Create(cwd, model, provider string) (string, error) {
	b := make([]byte, 4)
	rand.Read(b)
	id := hex.EncodeToString(b)
	_, err := s.db.ExecContext(context.Background(), `INSERT INTO sessions (id, created_at, updated_at, cwd, model, provider) VALUES (?,?,?,?,?,?)`,
		id, now(), now(), cwd, model, provider)
	return id, err
}

func (s *Store) Save(id string, from int, msgs []ai.Message, model, provider string) error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for i := from; i < len(msgs); i++ {

		if msgs[i].Role == "" {
			continue
		}
		data, err := json.Marshal(msgs[i])
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(context.Background(), `INSERT OR REPLACE INTO messages (session_id, seq, role, content) VALUES (?,?,?,?)`,
			id, i, msgs[i].Role, string(data)); err != nil {
			return err
		}
	}
	title := ""
	for _, m := range msgs {
		if m.Role == "user" {
			title = truncate(strings.Join(strings.Fields(m.TextContent()), " "), 64)
			break
		}
	}
	if _, err := tx.ExecContext(context.Background(), `UPDATE sessions SET updated_at=?, model=?, provider=?, title=CASE WHEN title='' THEN ? ELSE title END WHERE id=?`,
		now(), model, provider, title, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Load(idOrPrefix string) (Meta, []ai.Message, error) {
	rows, err := s.db.QueryContext(context.Background(), `SELECT id, title, model, provider, cwd, goal, forked_from, fork_seq, tags, pinned, effort, usage_in, usage_cached, usage_out, task_id, sub_usage, updated_at FROM sessions WHERE id LIKE ?||'%' LIMIT 3`, idOrPrefix)
	if err != nil {
		return Meta{}, nil, err
	}
	metas, err := scanMetas(rows)
	if err != nil {
		return Meta{}, nil, err
	}
	switch len(metas) {
	case 0:
		return Meta{}, nil, fmt.Errorf("no session matching %q", idOrPrefix)
	case 1:
	default:
		return Meta{}, nil, fmt.Errorf("session id %q is ambiguous", idOrPrefix)
	}
	meta := metas[0]
	msgs, err := s.loadMessages(meta.ID)
	if err != nil {
		return Meta{}, nil, err
	}
	return meta, msgs, nil
}

func (s *Store) loadMessages(id string) ([]ai.Message, error) {

	var count int
	_ = s.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM messages WHERE session_id=?`, id).Scan(&count)

	mrows, err := s.db.QueryContext(context.Background(), `SELECT content FROM messages WHERE session_id=? ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = mrows.Close() }()
	msgs := make([]ai.Message, 0, count)
	for mrows.Next() {
		var data string
		if err := mrows.Scan(&data); err != nil {
			return nil, err
		}
		var m ai.Message
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return answerDanglingToolCalls(applyCompaction(s.db, id, msgs)), mrows.Err()
}

func applyCompaction(db *sql.DB, sessionID string, msgs []ai.Message) []ai.Message {
	var cutoff int
	var summary string
	err := db.QueryRowContext(context.Background(), `SELECT cutoff, summary FROM compactions WHERE session_id=? ORDER BY seq DESC LIMIT 1`,
		sessionID).Scan(&cutoff, &summary)
	if err != nil || cutoff <= 1 || cutoff > len(msgs) {
		return msgs
	}

	fold := len(msgs)
	for i := cutoff; i < len(msgs); i++ {
		if msgs[i].Role != "system" {
			fold = i
			break
		}
	}
	out := make([]ai.Message, 0, len(msgs))
	out = append(out, msgs[0],
		ai.Message{Role: "system", Content: "Summary of the conversation so far:\n\n" + summary})

	var prior []ai.Message
	for i := 1; i < fold; i++ {
		if msgs[i].Role == "system" {
			prior = append(prior, msgs[i])
		}
	}
	if len(prior) > 0 {
		out = append(out, prior[len(prior)-1])
	}
	return append(out, msgs[fold:]...)
}

func answerDanglingToolCalls(msgs []ai.Message) []ai.Message {
	answered := make(map[string]bool, len(msgs))
	dangling := false
	for _, m := range msgs {
		if m.Role == "tool" {
			answered[m.ToolCallID] = true
		}
	}
	for _, m := range msgs {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				dangling = dangling || !answered[tc.ID]
			}
		}
	}
	if !dangling {
		return msgs
	}
	out := make([]ai.Message, 0, len(msgs)+4)
	for _, m := range msgs {
		out = append(out, m)
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.ToolCalls {
			if !answered[tc.ID] {
				out = append(out, ai.Message{
					Role:       "tool",
					Content:    "Error: tool call interrupted — the session ended before a result was recorded",
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
				})
			}
		}
	}
	return out
}

func (s *Store) Recent(n int) ([]Meta, error) {
	rows, err := s.db.QueryContext(context.Background(), `SELECT id, title, model, provider, cwd, goal, forked_from, fork_seq, tags, pinned, effort, usage_in, usage_cached, usage_out, task_id, sub_usage, updated_at FROM sessions
		WHERE EXISTS (SELECT 1 FROM messages WHERE session_id = sessions.id)
		ORDER BY updated_at DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	return scanMetas(rows)
}

func (s *Store) LatestInDir(dir string) (Meta, error) {
	row := s.db.QueryRowContext(context.Background(), `SELECT id, title, model, provider, cwd, goal, forked_from, fork_seq, tags, pinned, effort, usage_in, usage_cached, usage_out, task_id, sub_usage, updated_at FROM sessions
		WHERE cwd = ? AND task_id = '' AND EXISTS (SELECT 1 FROM messages WHERE session_id = sessions.id)
		ORDER BY updated_at DESC LIMIT 1`, dir)
	return scanMeta(row)
}

func (s *Store) UserHistory(limit int) ([]string, error) {
	rows, err := s.db.QueryContext(context.Background(), `SELECT m.content FROM messages m
		JOIN sessions s ON s.id = m.session_id
		WHERE m.role='user'
		ORDER BY s.updated_at DESC, m.seq DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	seen := map[string]bool{}
	var out []string
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var msg ai.Message
		if err := json.Unmarshal([]byte(data), &msg); err != nil {
			continue
		}
		if !msg.Authored {
			continue
		}
		content := strings.TrimSpace(msg.TextContent())
		if content == "" || seen[content] {
			continue
		}
		seen[content] = true
		out = append(out, content)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}

func (s *Store) LastExchange(id string) (user, assistant string) {
	for _, q := range []struct {
		role string
		dst  *string
	}{{"user", &user}, {"assistant", &assistant}} {
		var data string
		if err := s.db.QueryRowContext(context.Background(), `SELECT content FROM messages WHERE session_id=? AND role=? ORDER BY seq DESC LIMIT 1`,
			id, q.role).Scan(&data); err == nil {
			var m ai.Message
			if json.Unmarshal([]byte(data), &m) == nil {
				*q.dst = m.TextContent()
			}
		}
	}
	return user, assistant
}

func (s *Store) ClearMessages(id string) error {
	_, err := s.db.ExecContext(context.Background(), `DELETE FROM messages WHERE session_id=?`, id)
	return err
}

func (s *Store) DeleteFrom(id string, from int) error {
	_, err := s.db.ExecContext(context.Background(), `DELETE FROM messages WHERE session_id=? AND seq>=?`, id, from)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(context.Background(), `DELETE FROM snapshots WHERE session_id=? AND seq>=?`, id, from)
	return err
}

func (s *Store) SetSnapshot(id string, seq int, ref string) error {
	if ref == "" {
		_, err := s.db.ExecContext(context.Background(), `DELETE FROM snapshots WHERE session_id=? AND seq=?`, id, seq)
		return err
	}
	_, err := s.db.ExecContext(context.Background(), `INSERT OR REPLACE INTO snapshots (session_id, seq, ref, created_at) VALUES (?,?,?,?)`,
		id, seq, ref, now())
	return err
}

func (s *Store) Snapshots(id string) map[int]string {
	rows, err := s.db.QueryContext(context.Background(), `SELECT seq, ref FROM snapshots WHERE session_id=?`, id)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	out := map[int]string{}
	for rows.Next() {
		var seq int
		var ref string
		if rows.Scan(&seq, &ref) == nil {
			out[seq] = ref
		}
	}
	if rows.Err() != nil {
		return nil
	}
	return out
}

type Schedule struct {
	ID       int
	Schedule string
	Prompt   string
	Anchor   time.Time
	LastFire time.Time
}

func (s *Store) AddSchedule(sessionID, schedule, prompt string, anchor time.Time) (int, error) {
	var id int
	err := s.db.QueryRowContext(context.Background(), `INSERT INTO schedules (session_id, id, schedule, prompt, anchor, created_at)
		SELECT ?, COALESCE(MAX(id),0)+1, ?, ?, ?, ? FROM schedules WHERE session_id=? RETURNING id`,
		sessionID, schedule, prompt, anchor.UTC().Format(time.RFC3339), now(), sessionID).Scan(&id)
	return id, err
}

func (s *Store) Schedules(sessionID string) []Schedule {
	rows, err := s.db.QueryContext(context.Background(), `SELECT id, schedule, prompt, anchor, last_fire FROM schedules WHERE session_id=? ORDER BY id`, sessionID)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	var out []Schedule
	for rows.Next() {
		var sc Schedule
		var anchor, lastFire string
		if rows.Scan(&sc.ID, &sc.Schedule, &sc.Prompt, &anchor, &lastFire) != nil {
			continue
		}
		sc.Anchor, _ = time.Parse(time.RFC3339, anchor)
		sc.LastFire, _ = time.Parse(time.RFC3339, lastFire)
		out = append(out, sc)
	}
	if rows.Err() != nil {
		return nil
	}
	return out
}

func (s *Store) MarkFired(sessionID string, id int, at time.Time) error {
	_, err := s.db.ExecContext(context.Background(), `UPDATE schedules SET last_fire=? WHERE session_id=? AND id=?`,
		at.UTC().Format(time.RFC3339), sessionID, id)
	return err
}

func (s *Store) DeleteSchedule(sessionID string, id int) error {
	_, err := s.db.ExecContext(context.Background(), `DELETE FROM schedules WHERE session_id=? AND id=?`, sessionID, id)
	return err
}

func (s *Store) ClearSnapshots(id string) error {
	_, err := s.db.ExecContext(context.Background(), `DELETE FROM snapshots WHERE session_id=?`, id)
	return err
}

type Compaction struct {
	Seq     int
	Cutoff  int
	Summary string
	Model   string
	Usage   ai.Usage
}

func (s *Store) RecordCompaction(id string, cutoff int, summary, model string, usage ai.Usage) error {
	u, err := json.Marshal(usage)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(context.Background(), `INSERT INTO compactions (session_id, seq, cutoff, summary, created_at, model, usage)
		SELECT ?, COALESCE(MAX(seq),0)+1, ?, ?, ?, ?, ? FROM compactions WHERE session_id=?`,
		id, cutoff, summary, now(), model, string(u), id)
	return err
}

func (s *Store) Compactions(id string) []Compaction {
	rows, err := s.db.QueryContext(context.Background(), `SELECT seq, cutoff, summary, model, usage FROM compactions WHERE session_id=? ORDER BY seq`, id)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	var out []Compaction
	for rows.Next() {
		var c Compaction
		var u string
		if rows.Scan(&c.Seq, &c.Cutoff, &c.Summary, &c.Model, &u) == nil {
			if u != "" {
				_ = json.Unmarshal([]byte(u), &c.Usage)
			}
			out = append(out, c)
		}
	}
	if rows.Err() != nil {
		return nil
	}
	return out
}

func (s *Store) DeleteCompaction(id string, seq int) error {
	_, err := s.db.ExecContext(context.Background(), `DELETE FROM compactions WHERE session_id=? AND seq=?`, id, seq)
	return err
}

func (s *Store) RawMessages(id string) []ai.Message {
	rows, err := s.db.QueryContext(context.Background(), `SELECT content FROM messages WHERE session_id=? ORDER BY seq`, id)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	var msgs []ai.Message
	for rows.Next() {
		var data string
		if rows.Scan(&data) != nil {
			continue
		}
		var m ai.Message
		if json.Unmarshal([]byte(data), &m) == nil {
			msgs = append(msgs, m)
		}
	}
	if rows.Err() != nil {
		return nil
	}
	return msgs
}

func (s *Store) SetTitle(id, title string) error {
	_, err := s.db.ExecContext(context.Background(), `UPDATE sessions SET title=? WHERE id=?`, title, id)
	return err
}

func (s *Store) Fork(srcID string, uptoSeq int, title string) (string, error) {
	b := make([]byte, 4)
	rand.Read(b)
	newID := hex.EncodeToString(b)
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO sessions (id, created_at, updated_at, cwd, model, provider, title, goal, forked_from, fork_seq, effort)
		SELECT ?, ?, ?, cwd, model, provider, ?, goal, ?, ?, effort FROM sessions WHERE id=?`,
		newID, now(), now(), title, srcID, uptoSeq, srcID); err != nil {
		return "", err
	}
	if uptoSeq > 0 {
		if _, err := tx.ExecContext(context.Background(), `INSERT INTO messages (session_id, seq, role, content)
			SELECT ?, seq, role, content FROM messages WHERE session_id=? AND seq <= ?`,
			newID, srcID, uptoSeq); err != nil {
			return "", err
		}
	}
	return newID, tx.Commit()
}

func (s *Store) SetTags(id string, tags []string) error {
	_, err := s.db.ExecContext(context.Background(), `UPDATE sessions SET tags=? WHERE id=?`, strings.Join(tags, ","), id)
	return err
}

func (s *Store) SetPinned(id string, pinned bool) error {
	v := 0
	if pinned {
		v = 1
	}
	_, err := s.db.ExecContext(context.Background(), `UPDATE sessions SET pinned=? WHERE id=?`, v, id)
	return err
}

func (s *Store) ForksOf(id string) ([]Meta, error) {
	rows, err := s.db.QueryContext(context.Background(), `SELECT id, title, model, provider, cwd, goal, forked_from, fork_seq, tags, pinned, effort, usage_in, usage_cached, usage_out, task_id, sub_usage, updated_at
		FROM sessions WHERE forked_from=? ORDER BY updated_at DESC`, id)
	if err != nil {
		return nil, err
	}
	return scanMetas(rows)
}

func (s *Store) ForkTitle(base string) (string, error) {
	if base == "" {
		base = "session"
	}

	base = strings.TrimSpace(base)
	if i := strings.LastIndex(base, " (fork #"); i > 0 {
		var n0 int
		var rest string
		n, err := fmt.Sscanf(base[i:], " (fork #%d)%s", &n0, &rest)
		if n0 > 0 && rest == "" && (err == nil || errors.Is(err, io.EOF)) && n >= 1 {
			base = base[:i]
		}
	}
	rows, err := s.db.QueryContext(context.Background(), `SELECT title FROM sessions WHERE title = ? OR title LIKE ? ESCAPE '\'`,
		base, likeEscape(base)+` (fork #%)`)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return "", err
		}
		var num int
		var rest string

		if nf, err := fmt.Sscanf(t, base+" (fork #%d)%s", &num, &rest); num > n && rest == "" && nf >= 1 && (err == nil || errors.Is(err, io.EOF)) {
			n = num
		}
	}
	return fmt.Sprintf("%s (fork #%d)", base, n+1), rows.Err()
}

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func scanMeta(row interface{ Scan(dest ...any) error }) (Meta, error) {
	var m Meta
	var updated, tags, subUsage string
	var pinned int
	if err := row.Scan(&m.ID, &m.Title, &m.Model, &m.Provider, &m.CWD, &m.Goal,
		&m.ForkedFrom, &m.ForkSeq, &tags, &pinned, &m.Effort,
		&m.UsageIn, &m.UsageCached, &m.UsageOut, &m.TaskID, &subUsage, &updated); err != nil {
		return Meta{}, err
	}
	if tags != "" {
		m.Tags = strings.Split(tags, ",")
	}
	if subUsage != "" {
		_ = json.Unmarshal([]byte(subUsage), &m.SubUsage)
	}
	m.Pinned = pinned != 0
	m.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return m, nil
}

func scanMetas(rows *sql.Rows) ([]Meta, error) {
	defer func() { _ = rows.Close() }()
	var out []Meta
	for rows.Next() {
		m, err := scanMeta(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}
