package imaging

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Second-round review of the slow-download fixes. TestRecheck* fail on the reviewed tree;
// TestGuard* pass and pin behaviour the review attacked and found sound.

const recheckSubmitReason = "provider did not respond within 180 seconds"

type recheckRun struct {
	dir     string
	scope   Scope
	reg     *engineRegistry
	adapter *engineAdapter
	jobID   string
}

// recheckFirstRun creates one job, runs it until ready says so, then closes the process.
func recheckFirstRun(t *testing.T, supportsReconcile bool, submit func(context.Context, ExecuteRequest) (RemoteResult, error), ready func(ImageJob) bool) *recheckRun {
	t.Helper()
	r := &recheckRun{dir: t.TempDir(), scope: Scope{OwnerID: "engine-owner", BackendID: "engine-backend"}, adapter: &engineAdapter{submit: submit}}
	store, err := OpenStore(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.CreateProject(context.Background(), r.scope, "engine", "")
	if err != nil {
		t.Fatal(err)
	}
	target := SnapshotTarget(testTarget())
	target.Parameters = nil
	target.Capabilities.SupportsReconcile = supportsReconcile
	r.reg = &engineRegistry{target: target}
	svc := NewService(store, r.reg, nil, r.options())
	sched := NewScheduler(svc)
	j, _, err := svc.CreateJob(context.Background(), r.scope, "recheck", CreateJobRequest{ProjectID: p.ID, TargetID: target.ID, Operation: OperationGenerate, Prompt: "test", Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	r.jobID = j.ID
	if err = sched.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	engineEventually(t, func() bool {
		got, e := svc.GetJob(context.Background(), r.scope, j.ID)
		return e == nil && ready(got)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = sched.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *recheckRun) options() ServiceOptions {
	return ServiceOptions{
		AssetSink:       &engineSink{},
		AdapterResolver: func(context.Context, Scope, TargetSnapshot) (Adapter, error) { return r.adapter, nil },
		PollInterval:    time.Millisecond, MaxPollInterval: 5 * time.Millisecond,
	}
}

// restart reopens the store and runs the job to the wanted terminal status.
func (r *recheckRun) restart(t *testing.T, want JobStatus) ImageJob {
	t.Helper()
	store, err := OpenStore(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	svc := NewService(store, r.reg, nil, r.options())
	sched := NewScheduler(svc)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sched.Close(ctx)
	})
	if err = sched.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return awaitEnginePatiently(t, svc, r.scope, r.jobID, want)
}

// Attempt 1 was never sent and says so. Attempt 2 may have reached the provider when the app
// closed. The item still carries attempt 1's reason, so after a restart the job's unknown result
// claims the request was not sent, which is the opposite of what an unknown result warns about.
func TestRecheckNotSentReasonDoesNotDescribeTheLaterAttemptAfterRestart(t *testing.T) {
	const notSent = "request was cancelled or timed out before it was sent"
	var calls atomic.Int32
	second := make(chan struct{})
	r := recheckFirstRun(t, false, func(ctx context.Context, _ ExecuteRequest) (RemoteResult, error) {
		if calls.Add(1) == 1 {
			return RemoteResult{State: "failed"}, &AdapterError{Code: CodeUnavailable, Delivery: DeliveryNotSent, Reason: notSent}
		}
		close(second)
		<-ctx.Done()
		return RemoteResult{}, &AdapterError{Code: CodeResultUnknown, Delivery: DeliveryUncertain, Cause: ctx.Err()}
	}, func(ImageJob) bool {
		select {
		case <-second:
			return true
		default:
			return false
		}
	})
	got := r.restart(t, JobResultUnknown)
	if got.Error == nil || got.Error.Reason == notSent {
		t.Fatalf("reason = %q: attempt 1's phrase now describes attempt 2, whose delivery is unknown", recheckReason(got))
	}
}

func recheckReason(j ImageJob) string {
	if j.Error == nil {
		return ""
	}
	return j.Error.Reason
}

func recheckUncertainWithHandle(context.Context, ExecuteRequest) (RemoteResult, error) {
	return RemoteResult{State: "unknown", Handle: "task-1"}, &AdapterError{Code: CodeResultUnknown, Delivery: DeliveryUncertain, Reason: recheckSubmitReason}
}

// Fix 3 covers the failed-lookup branch and fix 4 added the explicit "unknown" branch, which only
// reads the attempt. Attempt reasons do not survive a restart, so the same poll answer gives a
// different reason depending on whether the app was restarted.
func TestRecheckExplicitUnknownKeepsTheSubmitReasonAcrossRestart(t *testing.T) {
	answer := func(context.Context, string) (RemoteResult, error) {
		return RemoteResult{State: "unknown", Handle: "task-1", ErrorCode: CodeNoImageOutput}, nil
	}
	t.Run("uninterrupted", func(t *testing.T) {
		s, q, a, scope, req := engineFixture(t)
		a.submit = recheckUncertainWithHandle
		a.poll = answer
		j, _, err := s.CreateJob(context.Background(), scope, "uninterrupted", req)
		if err != nil {
			t.Fatal(err)
		}
		q.Start(context.Background())
		got := awaitEnginePatiently(t, s, scope, j.ID, JobResultUnknown)
		if got.Error == nil || got.Error.Reason != recheckSubmitReason {
			t.Fatalf("control: %+v", got.Error)
		}
	})
	t.Run("restarted", func(t *testing.T) {
		r := recheckFirstRunWithPoll(t, recheckUncertainWithHandle)
		r.adapter.mu.Lock()
		r.adapter.poll = answer
		r.adapter.mu.Unlock()
		got := r.restart(t, JobResultUnknown)
		if got.Error == nil || got.Error.Reason != recheckSubmitReason {
			t.Fatalf("reason after restart = %q, want %q as the uninterrupted run reports", recheckReason(got), recheckSubmitReason)
		}
	})
}

// recheckFirstRunWithPoll keeps the first process from settling the job before it closes.
func recheckFirstRunWithPoll(t *testing.T, submit func(context.Context, ExecuteRequest) (RemoteResult, error)) *recheckRun {
	t.Helper()
	polling := make(chan struct{})
	var once atomic.Bool
	dir := t.TempDir()
	r := &recheckRun{dir: dir, scope: Scope{OwnerID: "engine-owner", BackendID: "engine-backend"}, adapter: &engineAdapter{submit: submit}}
	r.adapter.poll = func(ctx context.Context, _ string) (RemoteResult, error) {
		if once.CompareAndSwap(false, true) {
			close(polling)
		}
		<-ctx.Done()
		return RemoteResult{}, ctx.Err()
	}
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.CreateProject(context.Background(), r.scope, "engine", "")
	if err != nil {
		t.Fatal(err)
	}
	target := SnapshotTarget(testTarget())
	target.Parameters = nil
	r.reg = &engineRegistry{target: target}
	svc := NewService(store, r.reg, nil, r.options())
	sched := NewScheduler(svc)
	j, _, err := svc.CreateJob(context.Background(), r.scope, "recheck", CreateJobRequest{ProjectID: p.ID, TargetID: target.ID, Operation: OperationGenerate, Prompt: "test", Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	r.jobID = j.ID
	if err = sched.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-polling:
	case <-time.After(10 * time.Second):
		t.Fatal("first poll never started")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = sched.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	return r
}

// The "unknown" branch does not call PublicError itself, yet nothing raw reaches the sidecar.
func TestGuardExplicitUnknownReasonIsSanitizedBeforeItIsStored(t *testing.T) {
	const secret = "SECRETVALUE"
	for _, tc := range []struct {
		state string
		want  JobStatus
	}{{"failed", JobFailed}, {"unknown", JobResultUnknown}} {
		t.Run(tc.state, func(t *testing.T) {
			s, q, a, scope, req := engineFixture(t)
			a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
				return RemoteResult{State: tc.state, Handle: "task-1", ErrorCode: CodeUnavailable, ErrorReason: "upstream said https://x.test/a?sig=" + secret}, nil
			}
			j, _, err := s.CreateJob(context.Background(), scope, "raw-"+tc.state, req)
			if err != nil {
				t.Fatal(err)
			}
			q.Start(context.Background())
			awaitEnginePatiently(t, s, scope, j.ID, tc.want)
			b, err := os.ReadFile(filepath.Join(s.store.dir, reasonsFileName))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), secret) {
				t.Fatalf("%s holds the unsanitized reason: %s", reasonsFileName, b)
			}
		})
	}
}

// saveError lives in one finish call, so one item's slow phrase cannot reach another item.
func TestGuardSlowPhraseStaysOnItsOwnItem(t *testing.T) {
	s, q, a, scope, req := engineFixture(t)
	req.Count = 2
	var calls atomic.Int32
	a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
		name := "slow"
		if calls.Add(1) == 2 {
			name = "http"
		}
		return RemoteResult{State: "completed", Outputs: []OutputSource{reviewSlowOutput(name)}}, nil
	}
	s.options.AssetSink = engineSinkFunc(func(_ context.Context, _ Scope, o OutputSource) (ImageAsset, error) {
		if strings.Contains(o.URL, "slow") {
			return ImageAsset{}, reviewSpentSlow()
		}
		return ImageAsset{}, &ImageError{Code: CodeUnavailable, Reason: "image host returned HTTP 404"}
	})
	j, _, err := s.CreateJob(context.Background(), scope, "two-items", req)
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	got := awaitEnginePatiently(t, s, scope, j.ID, JobFailed)
	reasons := map[string]int{}
	for _, item := range got.Items {
		if item.Error == nil || item.Error.Code != CodeOutputSaveFailed {
			t.Fatalf("item error = %+v", item.Error)
		}
		reasons[item.Error.Reason]++
	}
	if reasons[reasonSlowDirectDownload] != 1 || reasons["image host returned HTTP 404"] != 1 {
		t.Fatalf("reasons = %v", reasons)
	}
}

// A restored reason must not outlive a poll that gave its own answer.
func TestGuardRestoredReasonDoesNotReachANewAnswer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		poll   func(context.Context, string) (RemoteResult, error)
		status JobStatus
		reason string
	}{
		{"completed", func(context.Context, string) (RemoteResult, error) {
			return RemoteResult{State: "completed", Outputs: []OutputSource{{Kind: "bytes", Data: []byte("image")}}}, nil
		}, JobSucceeded, ""},
		{"failed", func(context.Context, string) (RemoteResult, error) {
			return RemoteResult{State: "failed", ErrorCode: CodeNoImageOutput, ErrorReason: "provider refused the prompt"}, nil
		}, JobFailed, "provider refused the prompt"},
		// Lookups that keep failing say nothing new: the submit's reason stands, as without a restart.
		{"new lookup error", func(context.Context, string) (RemoteResult, error) {
			return RemoteResult{}, &AdapterError{Code: CodeUnavailable, Delivery: DeliveryUncertain, Reason: "task lookup returned HTTP 502"}
		}, JobResultUnknown, "provider did not respond within 180 seconds"},
		{"explicit reason", func(context.Context, string) (RemoteResult, error) {
			return RemoteResult{State: "unknown", ErrorReason: "provider lost the task"}, nil
		}, JobResultUnknown, "provider lost the task"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := recheckFirstRunWithPoll(t, recheckUncertainWithHandle)
			r.adapter.mu.Lock()
			r.adapter.poll = tc.poll
			r.adapter.mu.Unlock()
			got := r.restart(t, tc.status)
			if tc.reason == "" {
				if got.Error != nil {
					t.Fatalf("error = %+v", got.Error)
				}
				return
			}
			if got.Error == nil || got.Error.Reason != tc.reason {
				t.Fatalf("error = %+v, want reason %q", got.Error, tc.reason)
			}
		})
	}
}

// The explicit branch keeps the code, and only the truly unrecognised state keeps its phrase.
func TestGuardUnknownBranchVersusDefault(t *testing.T) {
	for _, tc := range []struct{ state, reason string }{
		{"unknown", "provider could not confirm the result"},
		{"bogus", "provider returned an unrecognised state"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			s, q, a, scope, req := engineFixture(t)
			a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
				return RemoteResult{State: tc.state, Handle: "task-1", ErrorCode: CodeNoImageOutput}, nil
			}
			j, _, err := s.CreateJob(context.Background(), scope, "branch-"+tc.state, req)
			if err != nil {
				t.Fatal(err)
			}
			q.Start(context.Background())
			got := awaitEnginePatiently(t, s, scope, j.ID, JobResultUnknown)
			if got.Error == nil || got.Error.Code != CodeResultUnknown || got.Error.Reason != tc.reason {
				t.Fatalf("error = %+v", got.Error)
			}
		})
	}
}
