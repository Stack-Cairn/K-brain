package session

// 跨会话的用量统计（GET /v1/usage/stats）：把会话目录里所有工作区的会话按天、按模型汇总。
//
// 口径：
//   - input 是 ai.Usage.PromptTokens，即一次请求的完整输入，已经包含缓存读取与缓存写入的部分
//     （anthropic_usage.go 把三者相加；Chat Completions、Responses、Gemini 的输入数本来就含缓存）。
//     cacheRead、cacheWrite 是其中的子集，与 UsageSummary、usage.updated 和 kn 的 /usage 卡片一致；
//     total = input + output，缓存部分不再重复相加。
//   - turn 是一条带 usage 的模型回复。时间取回复的 SentAt；旧回复没有 SentAt 时取它之前最近一条
//     有时间的消息，再没有就取会话的创建时间（其次是更新时间）。
//   - 消息里一条 usage 都没有的旧会话，把元数据里的合计（UsageIn 等，有 ModelUsage 时按模型）
//     记在会话最后活动那天，不计 turn。最后活动时间是最晚的消息时间，没有时用 UpdatedAt。
//   - 子代理：每个子代理的转录单独保存为 task-<父会话>-<任务> 会话（ForkedFrom 是父会话，TaskID 非空），
//     其中每条回复都带 usage；父会话元数据的 SubUsage 是同一份用量的合计。所以只统计子代理会话里的回复，
//     不再加 SubUsage；只有某个父会话的子代理会话里一条带 usage 的回复都没有时（旧版本没有保存转录），
//     才把 SubUsage 记在父会话最后活动那天。子代理的用量归到父会话，计入它的 sessions 与 longestSession。
//   - 分支（Fork）与历史复制会把消息连同 id、usage 原样拷到新会话：按消息 id 去重，先创建的会话计入。
//     没有 id 的旧消息在分支里按 ForkSeq 跳过继承来的部分。
//
// 每个会话文件解析一次后按路径缓存它的用量条目（只保留数字，不保留消息内容），文件大小或修改时间
// 变化才重新解析。统计不获取会话锁（s.mu）和会话目录的文件锁，正在运行的回合写会话不受影响；
// 会话文件总是整体改名替换，打开的文件句柄读到的是一份完整的旧文件或新文件。

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// UsageTokens 是一段用量的合计。Input 含缓存读写，CacheRead、CacheWrite 是其中的部分。
type UsageTokens struct {
	Input, Output, CacheRead, CacheWrite int64
	Turns                                int
}

func (t UsageTokens) Total() int64 { return t.Input + t.Output }

func (t *UsageTokens) add(e usageEntry) {
	t.Input += e.in
	t.Output += e.out
	t.CacheRead += e.read
	t.CacheWrite += e.write
	if e.turn {
		t.Turns++
	}
}

// Usage 把合计转成 ai.Usage，用于计价。
func (t UsageTokens) Usage() ai.Usage {
	return ai.Usage{PromptTokens: int(t.Input), CompletionTokens: int(t.Output), PromptCacheHitTokens: int(t.CacheRead), PromptCacheWriteTokens: int(t.CacheWrite)}
}

type UsageDay struct {
	Date string // YYYY-MM-DD，按请求的时区
	UsageTokens
	// Models 是各模型（不含 provider）当天的 total。
	Models map[string]int64
}

type UsageModel struct {
	Model, Provider string
	UsageTokens
}

type UsageSession struct {
	ID, Title string
	Total     int64
}

type UsageStatsOptions struct {
	Days     int
	Location *time.Location
	Now      time.Time
}

type UsageStats struct {
	Totals                                   UsageTokens
	Sessions                                 int
	Daily                                    []UsageDay   // 只有有用量的日子，按日期升序
	Models                                   []UsageModel // 按 total 降序
	ActiveDays, CurrentStreak, LongestStreak int
	TopModel                                 string
	LongestSession                           *UsageSession
}

// usageEntry 是一条带 usage 的回复，或旧会话元数据里的一份合计。
type usageEntry struct {
	key                  uint64 // 消息 id 的哈希，用于跨会话去重；0 表示不去重
	at                   int64  // Unix 秒
	label                int32  // sessionUsage.labels 的下标，"模型 @ provider"
	turn                 bool
	in, out, read, write int64
}

// sessionUsage 是一个会话文件解析出的用量，缓存在 usageStatsCache 里。
type sessionUsage struct {
	id, title string
	parent    string // 子代理会话的父会话 id
	createdAt time.Time
	labels    []string
	entries   []usageEntry
	// sub 是元数据 SubUsage 的合计，只在父会话的子代理会话都没有带 usage 的回复时计入。
	sub []usageEntry
	// msgUsage 表示有消息带 usage（包括从分支继承的消息）。
	msgUsage bool
}

type usageFileEntry struct {
	size    int64
	modTime time.Time
	usage   *sessionUsage // nil：文件无法解析，下次文件变化前不再重试
}

type usageStatsCache struct {
	// run 串行化统计请求，只会挡住另一个统计请求。
	run   sync.Mutex
	files map[string]usageFileEntry // 按会话文件路径
}

// UsageStats 汇总最近 opts.Days 天（含今天，按 opts.Location 分日）的用量。
func (s *Store) UsageStats(ctx context.Context, opts UsageStatsOptions) (UsageStats, error) {
	sessions, err := s.usageSessions(ctx)
	if err != nil {
		return UsageStats{}, err
	}
	return aggregateUsage(sessions, opts), nil
}

func (s *Store) usageSessions(ctx context.Context) ([]*sessionUsage, error) {
	c := &s.usageStats
	c.run.Lock()
	defer c.run.Unlock()
	files, err := s.scanSessionFiles()
	if err != nil {
		return nil, err
	}
	if c.files == nil {
		c.files = map[string]usageFileEntry{}
	}
	present := make(map[string]bool, len(files))
	out := make([]*sessionUsage, 0, len(files))
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		present[file.path] = true
		cached, ok := c.files[file.path]
		if !ok || cached.size != file.size || !cached.modTime.Equal(file.modTime) {
			cached = readSessionUsage(file.id, file.path)
			c.files[file.path] = cached
		}
		if cached.usage != nil {
			out = append(out, cached.usage)
		}
	}
	for path := range c.files {
		if !present[path] {
			delete(c.files, path)
		}
	}
	return out, nil
}

// readSessionUsage 逐行解析一个会话文件，只留下用量条目。缓存键取自打开的文件句柄，
// 与解析的内容一致；文件在解析期间被替换时，下次统计会看到新的大小或修改时间。
func readSessionUsage(id, path string) usageFileEntry {
	f, err := os.Open(path)
	if err != nil {
		return usageFileEntry{}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return usageFileEntry{}
	}
	entry := usageFileEntry{size: info.Size(), modTime: info.ModTime()}
	usage, err := parseSessionUsage(id, bufio.NewReader(f))
	if err == nil {
		entry.usage = usage
	}
	return entry
}

type usageMessage struct {
	seq    int
	id     string
	sentAt time.Time
	usage  *ai.Usage
	model  string
}

func parseSessionUsage(id string, reader *bufio.Reader) (*sessionUsage, error) {
	var meta *metadataRecord
	var msgs []usageMessage
	seen := map[int]bool{}
	for {
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		if line = bytes.TrimSpace(line); len(line) > 0 {
			var r record
			if err := json.Unmarshal(line, &r); err != nil {
				return nil, err
			}
			switch {
			case r.Type == "session_meta" && meta == nil:
				var m metadataRecord
				if err := json.Unmarshal(r.Payload, &m); err != nil {
					return nil, err
				}
				if m.ID != id {
					return nil, errors.New("session id mismatch")
				}
				meta = &m
			case meta == nil:
				return nil, errors.New("metadata must be first")
			case r.Type == "message":
				var m struct {
					Seq     int `json:"seq"`
					Message struct {
						ID     string     `json:"id"`
						Role   string     `json:"role"`
						SentAt *time.Time `json:"sent_at"`
						Usage  *ai.Usage  `json:"usage"`
						Model  string     `json:"model"`
					} `json:"message"`
				}
				if err := json.Unmarshal(r.Payload, &m); err != nil {
					return nil, err
				}
				if m.Seq < 0 || m.Message.Role == "" || seen[m.Seq] {
					continue
				}
				seen[m.Seq] = true
				msg := usageMessage{seq: m.Seq, id: m.Message.ID, usage: m.Message.Usage, model: m.Message.Model}
				if m.Message.SentAt != nil {
					msg.sentAt = *m.Message.SentAt
				}
				if m.Message.Role != "assistant" {
					// 只有回复带 usage；其余消息只用来推断时间。
					msg.usage, msg.id, msg.model = nil, "", ""
				}
				msgs = append(msgs, msg)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	if meta == nil {
		return nil, errors.New("missing metadata")
	}
	return buildSessionUsage(meta, msgs), nil
}

func buildSessionUsage(meta *metadataRecord, msgs []usageMessage) *sessionUsage {
	m := meta.Meta
	u := &sessionUsage{id: m.ID, title: m.Title, createdAt: meta.CreatedAt}
	if m.TaskID != "" {
		u.parent = m.ForkedFrom
	}
	fork := m.ForkedFrom != "" && m.TaskID == ""
	labels := map[string]int32{}
	label := func(name string) int32 {
		if name == "" {
			name = m.Model + " @ " + m.Provider
		}
		i, ok := labels[name]
		if !ok {
			i = int32(len(u.labels))
			labels[name] = i
			u.labels = append(u.labels, name)
		}
		return i
	}
	slices.SortFunc(msgs, func(a, b usageMessage) int { return cmp.Compare(a.seq, b.seq) })
	var last, latest time.Time
	for _, msg := range msgs {
		if !msg.sentAt.IsZero() {
			last = msg.sentAt
			if msg.sentAt.After(latest) {
				latest = msg.sentAt
			}
		}
		if msg.usage == nil {
			continue
		}
		u.msgUsage = true
		if fork && msg.id == "" && msg.seq <= m.ForkSeq {
			continue // 从源会话继承、无法按 id 去重的旧回复
		}
		at := msg.sentAt
		if at.IsZero() {
			at = firstTime(last, meta.CreatedAt, m.UpdatedAt)
		}
		e := entryFor(*msg.usage, at)
		e.key, e.turn, e.label = messageKey(msg.id), true, label(msg.model)
		u.entries = append(u.entries, e)
	}
	lastActive := firstTime(latest, m.UpdatedAt, meta.CreatedAt)
	if !u.msgUsage {
		if len(m.ModelUsage) > 0 {
			for _, name := range sortedLabels(m.ModelUsage) {
				if e := entryFor(m.ModelUsage[name], lastActive); e.in+e.out > 0 {
					e.label = label(name)
					u.entries = append(u.entries, e)
				}
			}
		} else if m.UsageIn+m.UsageOut > 0 {
			e := usageEntry{at: lastActive.Unix(), in: int64(m.UsageIn), out: int64(m.UsageOut), read: int64(m.UsageCached), write: int64(m.UsageCacheWrite)}
			e.label = label("")
			u.entries = append(u.entries, e)
		}
	}
	for _, name := range sortedLabels(m.SubUsage) {
		if e := entryFor(m.SubUsage[name], lastActive); e.in+e.out > 0 {
			e.label = label(name)
			u.sub = append(u.sub, e)
		}
	}
	return u
}

func entryFor(usage ai.Usage, at time.Time) usageEntry {
	return usageEntry{at: at.Unix(), in: int64(usage.PromptTokens), out: int64(usage.CompletionTokens), read: int64(usage.Cached()), write: int64(usage.CacheWrite())}
}

func sortedLabels(m map[string]ai.Usage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func firstTime(times ...time.Time) time.Time {
	for _, t := range times {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

func messageKey(id string) uint64 {
	if id == "" {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	return max(h.Sum64(), 1)
}

// civilDay 是 t 在 loc 里的日期，从 1970-01-01 起的天数。
func civilDay(t time.Time, loc *time.Location) int64 {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400
}

func dayString(day int64) string {
	return time.Unix(day*86400, 0).UTC().Format(time.DateOnly)
}

type usageAccum struct {
	tokens UsageTokens
	models map[string]int64
}

func aggregateUsage(sessions []*sessionUsage, opts UsageStatsOptions) UsageStats {
	loc := opts.Location
	if loc == nil {
		loc = time.Local
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	days := max(opts.Days, 1)
	today := civilDay(now, loc)
	first := today - int64(days) + 1

	// 先创建的会话先计入，分支与复制里同 id 的回复随后被去重。
	sessions = slices.Clone(sessions)
	slices.SortFunc(sessions, func(a, b *sessionUsage) int {
		if c := a.createdAt.Compare(b.createdAt); c != 0 {
			return c
		}
		return cmp.Compare(a.id, b.id)
	})
	byID := make(map[string]*sessionUsage, len(sessions))
	childUsage := map[string]bool{}
	for _, su := range sessions {
		byID[su.id] = su
		if su.parent != "" && su.msgUsage {
			childUsage[su.parent] = true
		}
	}

	var stats UsageStats
	seen := map[uint64]bool{}
	daily := map[int64]*usageAccum{}
	type modelKey struct{ model, provider string }
	models := map[modelKey]*UsageTokens{}
	modelTotals := map[string]int64{}
	sessionTotals := map[string]int64{}
	for _, su := range sessions {
		root := su.id
		if su.parent != "" && byID[su.parent] != nil {
			root = su.parent
		}
		entries := su.entries
		if su.parent == "" && !childUsage[su.id] && len(su.sub) > 0 {
			entries = append(slices.Clone(entries), su.sub...)
		}
		for _, e := range entries {
			day := civilDay(time.Unix(e.at, 0), loc)
			if day < first || day > today || e.in+e.out <= 0 {
				continue
			}
			if e.key != 0 {
				if seen[e.key] {
					continue
				}
				seen[e.key] = true
			}
			name, provider, _ := strings.Cut(su.labels[e.label], " @ ")
			total := e.in + e.out
			stats.Totals.add(e)
			acc := daily[day]
			if acc == nil {
				acc = &usageAccum{models: map[string]int64{}}
				daily[day] = acc
			}
			acc.tokens.add(e)
			acc.models[name] += total
			mk := modelKey{name, provider}
			if models[mk] == nil {
				models[mk] = &UsageTokens{}
			}
			models[mk].add(e)
			modelTotals[name] += total
			sessionTotals[root] += total
		}
	}

	dayKeys := make([]int64, 0, len(daily))
	for day := range daily {
		dayKeys = append(dayKeys, day)
	}
	slices.Sort(dayKeys)
	stats.Daily = make([]UsageDay, 0, len(dayKeys))
	run, prev := 0, int64(0)
	for i, day := range dayKeys {
		acc := daily[day]
		stats.Daily = append(stats.Daily, UsageDay{Date: dayString(day), UsageTokens: acc.tokens, Models: acc.models})
		if i > 0 && day == prev+1 {
			run++
		} else {
			run = 1
		}
		prev = day
		stats.LongestStreak = max(stats.LongestStreak, run)
	}
	stats.ActiveDays = len(dayKeys)
	// 当前连续天数到今天为止；今天还没有用量时从昨天算起。
	if n := len(dayKeys); n > 0 && dayKeys[n-1] >= today-1 {
		stats.CurrentStreak = 1
		for i := n - 1; i > 0 && dayKeys[i-1] == dayKeys[i]-1; i-- {
			stats.CurrentStreak++
		}
	}

	stats.Models = make([]UsageModel, 0, len(models))
	for mk, tokens := range models {
		stats.Models = append(stats.Models, UsageModel{Model: mk.model, Provider: mk.provider, UsageTokens: *tokens})
	}
	slices.SortFunc(stats.Models, func(a, b UsageModel) int {
		if c := cmp.Compare(b.Total(), a.Total()); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Model, b.Model); c != 0 {
			return c
		}
		return cmp.Compare(a.Provider, b.Provider)
	})
	var top int64
	for name, total := range modelTotals {
		if total > top || total == top && total > 0 && name < stats.TopModel {
			stats.TopModel, top = name, total
		}
	}

	stats.Sessions = len(sessionTotals)
	for id, total := range sessionTotals {
		best := stats.LongestSession
		if best == nil || total > best.Total || total == best.Total && id < best.ID {
			title := ""
			if su := byID[id]; su != nil {
				title = su.title
			}
			stats.LongestSession = &UsageSession{ID: id, Title: title, Total: total}
		}
	}
	return stats
}
