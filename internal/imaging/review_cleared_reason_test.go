package imaging

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	rcNotSent = "request was cancelled or timed out before it was sent"
	rcLookup  = "task lookup returned HTTP 502"
	rcFixed   = "provider could not confirm the result"
)

// rcRetryBlocked fails attempt 1 as not sent and holds attempt 2 until its context ends.
func rcRetryBlocked(second chan struct{}) func(context.Context, ExecuteRequest) (RemoteResult, error) {
	var calls atomic.Int32
	return func(ctx context.Context, _ ExecuteRequest) (RemoteResult, error) {
		if calls.Add(1) == 1 {
			return RemoteResult{State: "failed"}, &AdapterError{Code: CodeUnavailable, Delivery: DeliveryNotSent, Reason: rcNotSent}
		}
		close(second)
		<-ctx.Done()
		return RemoteResult{}, &AdapterError{Code: CodeResultUnknown, Delivery: DeliveryUncertain, Cause: ctx.Err()}
	}
}

func rcClosed(ch chan struct{}) func(ImageJob) bool {
	return func(ImageJob) bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
}

// The claim removes the reason from the sidecar on disk, and nothing brings it back on reopen.
func TestRegressionClaimClearsSidecarAndRestartDoesNotRestoreIt(t *testing.T) {
	second := make(chan struct{})
	r := recheckFirstRun(t, false, rcRetryBlocked(second), rcClosed(second))
	path := filepath.Join(r.dir, reasonsFileName)
	if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), rcNotSent) {
		t.Fatalf("sidecar still holds attempt 1's reason: %s", b)
	}
	store, err := OpenStore(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	item := store.data.Jobs[r.jobID].Value.Items[0]
	if item.Error != nil {
		t.Fatalf("item error after reopen = %+v", item.Error)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
}

// Crash after the WAL commit and before the sidecar write: the file still holds the old reason.
func TestRegressionStaleSidecarAfterCrashDoesNotComeBack(t *testing.T) {
	second := make(chan struct{})
	r := recheckFirstRun(t, false, rcRetryBlocked(second), rcClosed(second))
	var itemID string
	{
		store, err := OpenStore(r.dir)
		if err != nil {
			t.Fatal(err)
		}
		itemID = store.data.Jobs[r.jobID].Value.Items[0].ID
		store.Close()
	}
	raw, err := json.Marshal(map[string]jobReasons{r.jobID: {Items: map[string]string{itemID: rcNotSent}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.dir, reasonsFileName)
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	if e := store.data.Jobs[r.jobID].Value.Items[0].Error; e != nil {
		t.Fatalf("stale sidecar restored onto a cleared item: %+v", e)
	}
	store.Close()
	got := r.restart(t, JobResultUnknown)
	if got.Error == nil || got.Error.Reason == rcNotSent || got.Items[0].Error.Reason == rcNotSent {
		t.Fatalf("job %+v item %+v", got.Error, got.Items[0].Error)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), rcNotSent) {
		t.Fatalf("settling did not overwrite the stale sidecar: %s", b)
	}
}

// While attempt 2 runs nothing reports an error; its own outcome decides the job's error.
func TestRegressionJobErrorDuringRetryAndAfterIt(t *testing.T) {
	s, q, a, scope, req := engineFixture(t)
	second := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
		if calls.Add(1) == 1 {
			return RemoteResult{State: "failed"}, &AdapterError{Code: CodeUnavailable, Delivery: DeliveryNotSent, Reason: rcNotSent}
		}
		close(second)
		<-release
		return RemoteResult{State: "failed", ErrorCode: CodeNoImageOutput, ErrorReason: "attempt two reason"}, nil
	}
	j, _, err := s.CreateJob(context.Background(), scope, "retry-window", req)
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	<-second
	during, err := s.GetJob(context.Background(), scope, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if during.Status != JobRunning || during.Error != nil || during.Items[0].Error != nil {
		t.Fatalf("during retry: status %s job %+v item %+v", during.Status, during.Error, during.Items[0].Error)
	}
	close(release)
	got := awaitEnginePatiently(t, s, scope, j.ID, JobFailed)
	if got.Error == nil || got.Error.Reason != "attempt two reason" {
		t.Fatalf("job error = %+v", got.Error)
	}
}

// Cancelling while attempt 2 is in flight: a local cancel carries no stale failure, also after reopen.
func TestRegressionCancelInClaimWindowHasNoStaleError(t *testing.T) {
	s, q, a, scope, req := engineFixture(t)
	second := make(chan struct{})
	a.submit = rcRetryBlocked(second)
	j, _, err := s.CreateJob(context.Background(), scope, "cancel-window", req)
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	<-second
	cancelled, err := s.CancelJob(context.Background(), scope, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != JobCancelled || cancelled.CancelEffect != "local_only" {
		t.Fatalf("cancel: %+v", cancelled)
	}
	if cancelled.Error != nil || cancelled.Items[0].Error != nil {
		t.Fatalf("cancel kept an error: job %+v item %+v", cancelled.Error, cancelled.Items[0].Error)
	}
}

// Every attempt not sent: the terminal reason is the last attempt's and survives a restart.
func TestRegressionExhaustedRetriesKeepLastReasonAcrossRestart(t *testing.T) {
	var calls atomic.Int32
	r := recheckFirstRun(t, false, func(context.Context, ExecuteRequest) (RemoteResult, error) {
		reason := "not sent one"
		if calls.Add(1) == 3 {
			reason = "not sent three"
		}
		return RemoteResult{State: "failed"}, &AdapterError{Code: CodeUnavailable, Delivery: DeliveryNotSent, Reason: reason}
	}, func(j ImageJob) bool { return j.Status == JobFailed })
	got := r.restart(t, JobFailed)
	if got.Error == nil || got.Error.Reason != "not sent three" || got.Items[0].Error.Reason != "not sent three" {
		t.Fatalf("job %+v item %+v", got.Error, got.Items[0].Error)
	}
}

// rcRunUntil runs one more process until the poll closure signals, then closes it.
func (r *recheckRun) rcRunUntil(t *testing.T, reached chan struct{}) {
	t.Helper()
	store, err := OpenStore(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, r.reg, nil, r.options())
	sched := NewScheduler(svc)
	if err = sched.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("poll never reached")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = sched.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
}

// One failed lookup, then the provider answers "unknown" with nothing to add. The async adapter's
// unknown() helper returns exactly that. The answer came back fine, so a lookup error from an
// earlier poll does not explain it; no restart is involved.
func TestRegressionEarlierLookupErrorIsNotTheUnknownReason(t *testing.T) {
	s, q, a, scope, req := engineFixture(t)
	a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
		return RemoteResult{State: "pending", Handle: "task-1"}, nil
	}
	var polls atomic.Int32
	a.poll = func(context.Context, string) (RemoteResult, error) {
		if polls.Add(1) == 1 {
			return RemoteResult{}, &AdapterError{Code: CodeUnavailable, Delivery: DeliveryUncertain, Reason: rcLookup}
		}
		return RemoteResult{State: "unknown", Handle: "task-1", ErrorCode: CodeNoImageOutput}, nil
	}
	j, _, err := s.CreateJob(context.Background(), scope, "lookup-then-unknown", req)
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	got := awaitEnginePatiently(t, s, scope, j.ID, JobResultUnknown)
	if got.Error == nil || got.Error.Reason != rcFixed {
		t.Fatalf("reason = %q, want %q: the provider answered, the earlier 502 does not describe that answer", recheckReason(got), rcFixed)
	}
}

// Control for the order: with the attempt's reason in memory it wins over the lookup error.
func TestRegressionAttemptReasonBeatsLookupErrorWithoutRestart(t *testing.T) {
	s, q, a, scope, req := engineFixture(t)
	a.submit = recheckUncertainWithHandle
	var polls atomic.Int32
	a.poll = func(context.Context, string) (RemoteResult, error) {
		if polls.Add(1) == 1 {
			return RemoteResult{}, &AdapterError{Code: CodeUnavailable, Delivery: DeliveryUncertain, Reason: rcLookup}
		}
		return RemoteResult{State: "unknown", Handle: "task-1", ErrorCode: CodeNoImageOutput}, nil
	}
	j, _, err := s.CreateJob(context.Background(), scope, "attempt-wins", req)
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	got := awaitEnginePatiently(t, s, scope, j.ID, JobResultUnknown)
	if got.Error == nil || got.Error.Reason != recheckSubmitReason {
		t.Fatalf("reason = %q", recheckReason(got))
	}
}

// The same answers as the control, with a restart after the failed lookup.
func TestRegressionLookupErrorReplacesSubmitReasonAcrossRestart(t *testing.T) {
	r := recheckFirstRunWithPoll(t, recheckUncertainWithHandle)
	reached := make(chan struct{})
	var polls atomic.Int32
	r.adapter.mu.Lock()
	r.adapter.poll = func(ctx context.Context, _ string) (RemoteResult, error) {
		switch polls.Add(1) {
		case 1:
			return RemoteResult{}, &AdapterError{Code: CodeUnavailable, Delivery: DeliveryUncertain, Reason: rcLookup}
		case 2:
			close(reached)
		}
		<-ctx.Done()
		return RemoteResult{}, ctx.Err()
	}
	r.adapter.mu.Unlock()
	r.rcRunUntil(t, reached)
	r.adapter.mu.Lock()
	r.adapter.poll = func(context.Context, string) (RemoteResult, error) {
		return RemoteResult{State: "unknown", Handle: "task-1", ErrorCode: CodeNoImageOutput}, nil
	}
	r.adapter.mu.Unlock()
	got := r.restart(t, JobResultUnknown)
	if got.Error == nil || got.Error.Reason != recheckSubmitReason {
		t.Fatalf("reason after restart = %q, want %q as the uninterrupted run reports", recheckReason(got), recheckSubmitReason)
	}
}

// A pending answer after the restart wipes the restored reason before "unknown" arrives.
func TestRegressionPendingAfterRestartLosesSubmitReason(t *testing.T) {
	answers := func() func(context.Context, string) (RemoteResult, error) {
		var polls atomic.Int32
		return func(context.Context, string) (RemoteResult, error) {
			if polls.Add(1) == 1 {
				return RemoteResult{State: "pending", Handle: "task-1"}, nil
			}
			return RemoteResult{State: "unknown", Handle: "task-1", ErrorCode: CodeNoImageOutput}, nil
		}
	}
	t.Run("uninterrupted", func(t *testing.T) {
		s, q, a, scope, req := engineFixture(t)
		a.submit = recheckUncertainWithHandle
		a.poll = answers()
		j, _, err := s.CreateJob(context.Background(), scope, "pending-control", req)
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
		r.adapter.poll = answers()
		r.adapter.mu.Unlock()
		got := r.restart(t, JobResultUnknown)
		if got.Error == nil || got.Error.Reason != recheckSubmitReason {
			t.Fatalf("reason after restart = %q, want %q as the uninterrupted run reports", recheckReason(got), recheckSubmitReason)
		}
	})
}
