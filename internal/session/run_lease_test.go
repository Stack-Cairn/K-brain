package session

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

// 两个 Store 打开同一目录，模拟 kn 终端界面与 LiveAgent 的 backend 两个进程。
func openSharedStores(t *testing.T) (*Store, *Store) {
	t.Helper()
	home := t.TempDir()
	a, err := OpenProjectHome(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	b, err := OpenProjectHome(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return a, b
}

func leaseTestMessages(texts ...string) []ai.Message {
	msgs := []ai.Message{{Role: "system", Content: "system"}}
	for i, text := range texts {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs = append(msgs, ai.Message{Role: role, Content: text})
	}
	return msgs
}

func TestRunLeaseExcludesOtherProcessesAndGuardsWrites(t *testing.T) {
	backend, kn := openSharedStores(t)
	id, err := kn.Create(t.TempDir(), "model", "provider")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := kn.AcquireRun(id, "kn")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kn.AcquireRun(id, "kn"); !errors.Is(err, ErrRunElsewhere) {
		t.Fatalf("second lease in the same process = %v", err)
	}
	if _, own := kn.ExternalRun(id); own {
		t.Fatal("a process must not report its own lease as external")
	}
	run, busy := backend.ExternalRun(id)
	if !busy || run.Client != "kn" || run.StartedAt.IsZero() {
		t.Fatalf("backend sees run = %+v busy=%v", run, busy)
	}
	var elsewhere *RunElsewhereError
	if _, err := backend.AcquireRun(id, "backend"); !errors.As(err, &elsewhere) || elsewhere.Run.Client != "kn" {
		t.Fatalf("backend lease while kn runs = %v", err)
	}
	if err := backend.Save(id, 0, leaseTestMessages("from backend"), "model", "provider"); !errors.Is(err, ErrRunElsewhere) {
		t.Fatalf("backend save while kn runs = %v", err)
	}
	if err := kn.Save(id, 0, leaseTestMessages("from kn"), "model", "provider"); err != nil {
		t.Fatalf("lease holder save = %v", err)
	}
	if err := backend.Delete(id); !errors.Is(err, ErrRunElsewhere) {
		t.Fatalf("backend delete while kn runs = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("second release = %v", err)
	}
	if _, busy := backend.ExternalRun(id); busy {
		t.Fatal("released lease is still reported")
	}
	other, err := backend.AcquireRun(id, "backend")
	if err != nil {
		t.Fatalf("backend lease after release = %v", err)
	}
	defer other.Release()
	if run, busy := kn.ExternalRun(id); !busy || run.Client != "backend" {
		t.Fatalf("kn sees run = %+v busy=%v", run, busy)
	}
}

func TestHistoryStaleRefusesOverwritingAnotherProcess(t *testing.T) {
	backend, kn := openSharedStores(t)
	id, err := kn.Create(t.TempDir(), "model", "provider")
	if err != nil {
		t.Fatal(err)
	}
	first := leaseTestMessages("one", "two")
	if err := kn.Save(id, 0, first, "model", "provider"); err != nil {
		t.Fatal(err)
	}
	// backend 缓存了会话（如 LiveAgent 打开过它）。
	cached, msgs, err := backend.History(id, first[:1])
	if err != nil {
		t.Fatal(err)
	}
	// kn 随后继续了这个会话。
	knHistory, knMsgs, err := kn.History(id, first[:1])
	if err != nil {
		t.Fatal(err)
	}
	knMsgs = append(knMsgs, ai.Message{Role: "user", Content: "three"}, ai.Message{Role: "assistant", Content: "four"})
	if err := kn.SaveHistory(id, knHistory, knMsgs, "model", "provider"); err != nil {
		t.Fatal(err)
	}
	if stale, err := backend.HistoryStale(id, cached); err != nil || !stale {
		t.Fatalf("stale = %v, %v", stale, err)
	}
	msgs = append(msgs, ai.Message{Role: "user", Content: "from backend"})
	if err := backend.SaveHistory(id, cached, msgs, "model", "provider"); !errors.Is(err, ErrHistoryStale) {
		t.Fatalf("stale save = %v", err)
	}
	_, stored, err := backend.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(stored); got != 5 || stored[len(stored)-1].Content != "four" {
		t.Fatalf("kn's messages were overwritten: %+v", stored)
	}
	// 重新加载后 backend 接着磁盘上的记录写。
	fresh, freshMsgs, err := backend.History(id, first[:1])
	if err != nil {
		t.Fatal(err)
	}
	freshMsgs = append(freshMsgs, ai.Message{Role: "user", Content: "five"})
	if err := backend.SaveHistory(id, fresh, freshMsgs, "model", "provider"); err != nil {
		t.Fatalf("save after reload = %v", err)
	}
	if stale, _ := kn.HistoryStale(id, knHistory); !stale {
		t.Fatal("kn's history should now be stale")
	}
}

func TestSummaryChangesNoticeAnotherProcessWithinASecond(t *testing.T) {
	backend, kn := openSharedStores(t)
	backend.summaryBeaconPoll = 50 * time.Millisecond
	backend.summaryRunProbe = 100 * time.Millisecond
	baseline, err := backend.SummarySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	since := baseline.Cursor.Revision
	next := func() SummaryChange {
		t.Helper()
		start := time.Now()
		for time.Since(start) < 3*time.Second {
			result, err := backend.SummaryChanges(context.Background(), SummaryChangeRequest{Epoch: baseline.Cursor.Epoch, Since: since, Wait: 3 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			since = result.NextSince
			if len(result.Changes) > 0 {
				if elapsed := time.Since(start); elapsed > 2*time.Second {
					t.Fatalf("change took %v", elapsed)
				}
				return result.Changes[len(result.Changes)-1]
			}
		}
		t.Fatal("no change")
		return SummaryChange{}
	}
	// 让 backend 的扫描器先运行起来，像 LiveAgent 的长轮询那样。
	waiting := make(chan SummaryChange, 1)
	go func() { waiting <- next() }()
	time.Sleep(200 * time.Millisecond)

	id, err := kn.Create(t.TempDir(), "model", "provider")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := kn.AcquireRun(id, "kn")
	if err != nil {
		t.Fatal(err)
	}
	if err := kn.Save(id, 0, leaseTestMessages("hello from kn"), "model", "provider"); err != nil {
		t.Fatal(err)
	}
	change := <-waiting
	for change.MessageCount == 0 || change.ExternalRun == nil {
		change = next()
	}
	if change.ID != id || change.Kind != "upsert" || change.MessageCount != 2 || change.ExternalRun.Client != "kn" {
		t.Fatalf("running change = %+v", change)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	change = next()
	if change.ID != id || change.ExternalRun != nil {
		t.Fatalf("finished change = %+v", change)
	}
	page, err := backend.ListPage(context.Background(), PageOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 1 || page.ExternalRuns[0] != nil {
		t.Fatalf("page = %+v", page)
	}
}

// 进程崩溃时操作系统释放文件锁：残留的说明文件不会让会话一直显示为运行中。
func TestRunLeaseLeftoverOwnerFileIsNotARun(t *testing.T) {
	backend, kn := openSharedStores(t)
	id, err := kn.Create(t.TempDir(), "model", "provider")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := kn.AcquireRun(id, "kn")
	if err != nil {
		t.Fatal(err)
	}
	// 模拟崩溃：只释放锁，不删除说明文件。
	if err := lease.lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	if _, busy := backend.ExternalRun(id); busy {
		t.Fatal("unlocked lease reported as running")
	}
	again, err := backend.AcquireRun(id, "backend")
	if err != nil {
		t.Fatalf("lease after crash = %v", err)
	}
	if err := again.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreCloseReleasesHeldRunLeases(t *testing.T) {
	home := t.TempDir()
	a, err := OpenProjectHome(home)
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenProjectHome(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	id, err := a.Create(t.TempDir(), "model", "provider")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.AcquireRun(id, "kn"); err != nil {
		t.Fatal(err)
	}
	if _, busy := b.ExternalRun(id); !busy {
		t.Fatal("held lease not visible to the other store")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if run, busy := b.ExternalRun(id); busy {
		t.Fatalf("lease still held after Close: %+v", run)
	}
	// 锁文件不再被打开，可以删除（Windows 上打开的句柄会让删除失败）。
	lockPath, _ := b.runLeasePaths(id)
	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("remove lock file after Close: %v", err)
	}
}
