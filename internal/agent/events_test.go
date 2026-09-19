package agent

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func recorder(mu *sync.Mutex, got *[]string) Events {
	add := func(s string) {
		mu.Lock()
		*got = append(*got, s)
		mu.Unlock()
	}
	return Events{
		OnText:      func(s string) { add("text:" + s) },
		OnThink:     func(s string) { add("think:" + s) },
		OnToolStart: func(id, name, args string) { add("start:" + id + name + args) },
		OnToolEnd:   func(id, name, res string) { add("end:" + id + name + res) },
		OnSteer:     func(s string) { add("steer:" + s) },
		OnCompact:   func(took, kept int) { add("compact") },
		OnUsage:     func(u ai.Usage) { add("usage") },
	}
}

func fire(ev Events) {
	ev.OnText("t")
	ev.OnThink("k")
	ev.OnToolStart("i", "n", "a")
	ev.OnToolEnd("i", "n", "r")
	ev.OnSteer("s")
	ev.OnCompact(1, 2)
	if ev.OnUsage != nil {
		ev.OnUsage(ai.Usage{})
	}
}

func TestFanInAllCallbacks(t *testing.T) {
	var mu sync.Mutex
	var a, b []string
	fire(FanIn(recorder(&mu, &a), Events{}, recorder(&mu, &b)))

	want := []string{"text:t", "think:k", "start:ina", "end:inr", "steer:s", "compact", "usage"}
	for _, got := range [][]string{a, b} {
		if len(got) != len(want) {
			t.Fatalf("fan-in delivered %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("event %d = %q, want %q", i, got[i], want[i])
			}
		}
	}
}

func TestEmitterBroadcastsToSubscribers(t *testing.T) {
	r := newTaskRegistry()
	task := &BackgroundTask{ID: "task-1", Status: TaskRunning, Done: make(chan struct{}), cancel: func() {}}
	r.tasks[task.ID] = task

	var mu sync.Mutex
	var a, b []string
	_, _, okA := r.SubscribeWithJournal(task.ID, recorder(&mu, &a))
	_, _, okB := r.SubscribeWithJournal(task.ID, recorder(&mu, &b))
	if !okA || !okB {
		t.Fatal("SubscribeWithJournal on a running task must report live")
	}
	if _, _, ok := r.SubscribeWithJournal("task-nope", Events{}); ok {
		t.Error("SubscribeWithJournal on an unknown task must fail")
	}

	fire(r.emitter(task.ID))
	mu.Lock()
	if len(a) != 6 || len(b) != 6 || a[0] != "text:t" || a[5] != "compact" {
		t.Errorf("subscribers saw %v / %v", a, b)
	}
	mu.Unlock()

	fire(r.emitter("task-nope"))

	r.settle(task.ID, TaskDone, "report")
	if _, _, ok := r.SubscribeWithJournal(task.ID, Events{}); ok {
		t.Error("SubscribeWithJournal on a settled task must not report live")
	}
}

func TestTaskRegistryUnknownIDsAndOrder(t *testing.T) {
	r := newTaskRegistry()
	if _, ok := r.Get("task-nope"); ok {
		t.Error("Get on an unknown task must report false")
	}
	if r.Cancel("task-nope") {
		t.Error("Cancel on an unknown task must report false")
	}
	r.settle("task-nope", TaskDone, "ignored")

	now := time.Now()
	for _, id := range []string{"task-10", "task-2", "task-1"} {
		r.tasks[id] = &BackgroundTask{ID: id, Status: TaskDone, StartedAt: now, Done: make(chan struct{}), cancel: func() {}}
	}
	r.tasks["task-3"] = &BackgroundTask{ID: "task-3", Status: TaskRunning, StartedAt: now.Add(time.Second), Done: make(chan struct{}), cancel: func() {}}
	got := r.List()
	want := []string{"task-1", "task-2", "task-10", "task-3"}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("List order = %v, want %v", got, want)
		}
	}

	if n := r.ClearSettled(); n != 3 {
		t.Errorf("ClearSettled cleared %d, want 3", n)
	}
	if list := r.List(); len(list) != 1 || list[0].ID != "task-3" {
		t.Errorf("running task must survive ClearSettled: %v", list)
	}
	if r.Cancel("task-1") {
		t.Error("Cancel on a cleared task must report false")
	}
}

func TestTasksLazyRegistry(t *testing.T) {
	a := New(nil, "m", 0, "sys")
	a.bg = nil
	r := a.Tasks()
	if r == nil || a.Tasks() != r {
		t.Fatal("Tasks must create once and return the same registry")
	}
}

func TestFanInPreservesEveryCallback(t *testing.T) {
	var events Events
	fields := reflect.ValueOf(&events).Elem()
	counts := make([]int, fields.NumField())
	for i := range fields.NumField() {
		field := fields.Field(i)
		field.Set(reflect.MakeFunc(field.Type(), func(args []reflect.Value) []reflect.Value {
			counts[i]++
			for _, arg := range args {
				switch arg.Kind() {
				case reflect.String:
					if arg.String() != "payload" {
						t.Errorf("callback %s lost string argument", fields.Type().Field(i).Name)
					}
				case reflect.Int:
					if arg.Int() != 7 {
						t.Errorf("callback %s lost integer argument", fields.Type().Field(i).Name)
					}
				}
			}
			return nil
		}))
	}
	fan := reflect.ValueOf(FanIn(events, Events{}, events))
	for i := range fan.NumField() {
		callback := fan.Field(i)
		if callback.IsNil() {
			t.Errorf("FanIn dropped %s", fan.Type().Field(i).Name)
			continue
		}
		args := make([]reflect.Value, callback.Type().NumIn())
		for j := range args {
			args[j] = reflect.New(callback.Type().In(j)).Elem()
			switch args[j].Kind() {
			case reflect.String:
				args[j].SetString("payload")
			case reflect.Int:
				args[j].SetInt(7)
			}
		}
		callback.Call(args)
		if counts[i] != 2 {
			t.Errorf("%s delivered %d times, want 2", fan.Type().Field(i).Name, counts[i])
		}
	}
}
