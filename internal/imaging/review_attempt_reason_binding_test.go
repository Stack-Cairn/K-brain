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

// rbSubmit fails attempt 1 as not sent, then attempt 2 as uncertain with no reason, which is what
// direct/response.go and the async adapter's failure(CodeResultUnknown, DeliveryUncertain) return.
func rbSubmit(retryAfter time.Duration) func(context.Context, ExecuteRequest) (RemoteResult, error) {
	var calls atomic.Int32
	return func(context.Context, ExecuteRequest) (RemoteResult, error) {
		if calls.Add(1) == 1 {
			return RemoteResult{State: "failed"}, &AdapterError{Code: CodeUnavailable, Delivery: DeliveryNotSent, Reason: rcNotSent, RetryAfter: retryAfter}
		}
		return RemoteResult{}, &AdapterError{Code: CodeResultUnknown, Delivery: DeliveryUncertain}
	}
}

// rbFirstRun stops with attempt 2 failed and its reconcile still in flight.
func rbFirstRun(t *testing.T, retryAfter time.Duration, observe func(dir string, j ImageJob)) *recheckRun {
	t.Helper()
	r := &recheckRun{dir: t.TempDir(), scope: Scope{OwnerID: "engine-owner", BackendID: "engine-backend"}, adapter: &engineAdapter{submit: rbSubmit(retryAfter)}}
	r.adapter.reconcile = func(ctx context.Context, _ string) (RemoteResult, error) {
		<-ctx.Done()
		return RemoteResult{}, ctx.Err()
	}
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
	target.Capabilities.SupportsReconcile = true
	r.reg = &engineRegistry{target: target}
	svc := NewService(store, r.reg, nil, r.options())
	sched := NewScheduler(svc)
	j, _, err := svc.CreateJob(context.Background(), r.scope, "binding", CreateJobRequest{ProjectID: p.ID, TargetID: target.ID, Operation: OperationGenerate, Prompt: "test", Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	r.jobID = j.ID
	if err = sched.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	engineEventually(t, func() bool {
		got, e := svc.GetJob(context.Background(), r.scope, j.ID)
		if e != nil {
			return false
		}
		if observe != nil {
			observe(r.dir, got)
		}
		return got.Items[0].AttemptCount == 2 && got.Items[0].Error != nil
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

func rbCheck(t *testing.T, r *recheckRun, reconcile func(context.Context, string) (RemoteResult, error)) {
	t.Helper()
	store, err := OpenStore(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	var attempt *ImageError
	for _, x := range store.data.Executions[r.jobID].Items {
		attempt = x.Attempts[1].Error
	}
	store.Close()
	if attempt == nil {
		t.Fatal("fixture: attempt 2 has no error")
	}
	if attempt.Reason == rcNotSent {
		t.Errorf("attempt 2 after reopen has attempt 1's reason %q", attempt.Reason)
	}
	r.adapter.mu.Lock()
	r.adapter.reconcile = reconcile
	r.adapter.mu.Unlock()
	got := r.restart(t, JobResultUnknown)
	if got.Error == nil || got.Error.Reason == rcNotSent || got.Items[0].Error.Reason == rcNotSent {
		t.Errorf("%s says %q, yet attempt 2 may have reached the provider and been billed", got.Error.Code, recheckReason(got))
	}
}

var rbAnswers = map[string]func(context.Context, string) (RemoteResult, error){
	"explicit unknown": func(context.Context, string) (RemoteResult, error) {
		return RemoteResult{State: "unknown", ErrorCode: CodeNoImageOutput}, nil
	},
	"lookup unsupported": nil,
}

// The file holds attempt 1's entry while attempt 2 failed without a reason. A downgrade reaches this
// state: released binaries retry without reading or writing the file. The entry has no attempt
// number, so it attaches to whichever attempt is last.
func TestRegressionStaleAttemptEntryDoesNotDescribeALaterAttempt(t *testing.T) {
	for name, answer := range rbAnswers {
		t.Run(name, func(t *testing.T) {
			r := rbFirstRun(t, 0, nil)
			store, err := OpenStore(r.dir)
			if err != nil {
				t.Fatal(err)
			}
			var itemID string
			for id := range store.data.Executions[r.jobID].Items {
				itemID = id
			}
			store.Close()
			raw, err := json.Marshal(map[string]jobReasons{r.jobID: {Attempts: map[string]attemptReason{itemID: {No: 1, Reason: rcNotSent}}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(r.dir, reasonsFileName), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			rbCheck(t, r, answer)
		})
	}
}

// Exactly one sidecar write fails, the claim's, because another process holds the file. The claim
// already removed the job's entry from memory, so attempt 2's reasonless failure finds nothing to
// save and the file keeps attempt 1's text through a clean shutdown. No crash and no downgrade.
func TestRegressionOneLostSidecarWriteLeavesAttemptOneReasonOnAttemptTwo(t *testing.T) {
	for name, answer := range rbAnswers {
		t.Run(name, func(t *testing.T) {
			var held *os.File
			released := false
			r := rbFirstRun(t, 400*time.Millisecond, func(dir string, j ImageJob) {
				path := filepath.Join(dir, reasonsFileName)
				if held == nil {
					if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), rcNotSent) {
						if held, err = os.Open(path); err != nil {
							t.Error(err)
						}
					}
				}
				// Readers wait for the store lock, so seeing attempt 2 means its claim's write has ended.
				if held != nil && !released && j.Items[0].AttemptCount == 2 {
					held.Close()
					released = true
				}
			})
			if !released {
				t.Fatal("fixture: the file was never held across the claim")
			}
			b, err := os.ReadFile(filepath.Join(r.dir, reasonsFileName))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), rcNotSent) {
				t.Skipf("holding the file did not block the replace on this platform: %s", b)
			}
			rbCheck(t, r, answer)
		})
	}
}
