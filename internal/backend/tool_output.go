package backend

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// 运行中命令的输出以 tool.status{tool_call_id,status:"running",message} 推送，
// message 是截至当前的输出尾部快照（不是增量），客户端每次整体替换即可，
// 断线重放也不会重复拼接。事件和其它事件一样写入会话事件日志（seq 必须连续），
// 所以这里合并快照并限制大小：同一调用最多每 toolOutputInterval 发一次，
// 每次最多 maxToolOutputBytes；会话历史只保存最终的 tool.result。
const (
	toolOutputInterval = 500 * time.Millisecond
	maxToolOutputBytes = 4 << 10
)

type toolOutputThrottle struct {
	mu       sync.Mutex
	interval time.Duration
	publish  func(id, output string)
	calls    map[string]*toolOutputState
	closed   bool
}

type toolOutputState struct {
	last    time.Time
	pending string
	dirty   bool
	timer   *time.Timer
	done    bool
}

func newToolOutputThrottle(interval time.Duration, publish func(id, output string)) *toolOutputThrottle {
	return &toolOutputThrottle{interval: interval, publish: publish, calls: map[string]*toolOutputState{}}
}

// update 记下最新快照：距上次发布已满间隔就立即发布，否则排一个尾随发布，
// 保证输出停住后最后一段也能送达。
func (t *toolOutputThrottle) update(id, snapshot string) {
	if id == "" {
		return
	}
	output := tailToolOutput(snapshot, maxToolOutputBytes)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	st := t.calls[id]
	if st == nil {
		st = &toolOutputState{}
		t.calls[id] = st
	}
	if st.done {
		return
	}
	st.pending, st.dirty = output, true
	if st.timer != nil {
		return
	}
	wait := t.interval - time.Since(st.last)
	if wait <= 0 {
		t.flushLocked(id, st)
		return
	}
	st.timer = time.AfterFunc(wait, func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		st.timer = nil
		if t.closed || st.done {
			return
		}
		t.flushLocked(id, st)
	})
}

// flushLocked 在持锁时发布，finish 因此一定排在已开始的发布之后，
// 运行输出不会晚于 tool.result 出现。
func (t *toolOutputThrottle) flushLocked(id string, st *toolOutputState) {
	if !st.dirty {
		return
	}
	output := st.pending
	st.pending, st.dirty, st.last = "", false, time.Now()
	t.publish(id, output)
}

// finish 在 tool.result 发布前调用：丢弃尚未发出的快照，之后的更新一律忽略。
func (t *toolOutputThrottle) finish(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.calls[id]
	if st == nil {
		st = &toolOutputState{}
		t.calls[id] = st
	}
	st.done, st.pending, st.dirty = true, "", false
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
}

// close 在运行结束时停掉所有尾随发布。
func (t *toolOutputThrottle) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	for _, st := range t.calls {
		st.done, st.pending, st.dirty = true, "", false
		if st.timer != nil {
			st.timer.Stop()
			st.timer = nil
		}
	}
}

// tailToolOutput 保留最多 limit 字节的尾部；尽量从整行开始，至少落在 UTF-8 字符边界上。
func tailToolOutput(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := len(s) - limit
	if i := strings.IndexByte(s[cut:], '\n'); i >= 0 && i < limit/2 {
		return s[cut+i+1:]
	}
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return s[cut:]
}
