package imaging

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// imageDownloadTimeout is a constant, so a short caller deadline stands in for the five minutes:
// retrieveWithin cannot tell them apart, it only asks ctx.Err().

func reviewSlowOutput(name string) OutputSource {
	return OutputSource{Kind: "url", URL: "https://images.review.test/" + name + ".png?sig=PRIVATE"}
}

func reviewTwoOutputs(a *engineAdapter) {
	a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
		return RemoteResult{State: "completed", Outputs: []OutputSource{reviewSlowOutput("first"), reviewSlowOutput("second")}}, nil
	}
}

func reviewOneURLOutput(a *engineAdapter, useProxy bool) {
	a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
		o := reviewSlowOutput("only")
		o.UseProxy = useProxy
		return RemoteResult{State: "completed", Outputs: []OutputSource{o}}, nil
	}
}

func reviewSpentSlow() error {
	return downloadBudgetSpent{&ImageError{Code: CodeUnavailable, Reason: reasonSlowDirectDownload}}
}

// the last failed output decides the reason, so the advice depends on output order.
func TestRegressionSlowHintDoesNotDependOnOutputOrder(t *testing.T) {
	for _, tc := range []struct {
		name          string
		first, second error
	}{
		{"slow-then-http", reviewSpentSlow(), &ImageError{Code: CodeUnavailable, Reason: "image host returned HTTP 404"}},
		{"http-then-slow", &ImageError{Code: CodeUnavailable, Reason: "image host returned HTTP 404"}, reviewSpentSlow()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, q, a, scope, r := engineFixture(t)
			reviewTwoOutputs(a)
			s.options.AssetSink = engineSinkFunc(func(_ context.Context, _ Scope, o OutputSource) (ImageAsset, error) {
				if strings.Contains(o.URL, "first") {
					return ImageAsset{}, tc.first
				}
				return ImageAsset{}, tc.second
			})
			j, _, err := s.CreateJob(context.Background(), scope, "order-"+tc.name, r)
			if err != nil {
				t.Fatal(err)
			}
			q.Start(context.Background())
			got := awaitEnginePatiently(t, s, scope, j.ID, JobFailed)
			if got.Error == nil || got.Error.Reason != reasonSlowDirectDownload {
				t.Fatalf("reason = %q, want the slow-direct advice whichever output failed last", got.Error.Reason)
			}
		})
	}
}

// the first output spent its budget on a slow direct route, then the job deadline cuts the
// second. The guard only looks at the last output's error, so the advice is lost.
func TestRegressionSlowHintSurvivesDeadlineOnNextOutput(t *testing.T) {
	s, q, a, scope, r := engineFixture(t)
	s.options.JobTimeout = 500 * time.Millisecond
	reviewTwoOutputs(a)
	s.options.AssetSink = engineSinkFunc(func(ctx context.Context, _ Scope, o OutputSource) (ImageAsset, error) {
		if strings.Contains(o.URL, "first") {
			return ImageAsset{}, reviewSpentSlow() // used its whole budget on the direct route
		}
		<-ctx.Done() // the second output is still downloading when the job deadline cancels it
		return ImageAsset{}, ctx.Err()
	})
	j, _, err := s.CreateJob(context.Background(), scope, "deadline-second", r)
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	got := awaitEnginePatiently(t, s, scope, j.ID, JobFailed)
	if got.Error == nil || got.Error.Reason != reasonSlowDirectDownload {
		t.Fatalf("reason = %q, want the slow-direct advice", got.Error.Reason)
	}
}

// only the slow-direct phrase stops the retries. A toggle-on download that used the whole
// budget is attempted three times, so the "5 minute total" becomes 15 minutes plus backoff.
func TestRegressionToggleOnDownloadThatSpentItsBudgetIsNotRetried(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	proxy := zz2Proxy(t, func(net.Conn, *http.Request) { <-block })
	zz2UseProxy(t, "http://"+proxy.addr)
	s, q, a, scope, r := engineFixture(t)
	reviewOneURLOutput(a, true)
	var calls atomic.Int32
	s.options.AssetSink = engineSinkFunc(func(ctx context.Context, _ Scope, o OutputSource) (ImageAsset, error) {
		calls.Add(1)
		budget, cancel := context.WithTimeout(ctx, 100*time.Millisecond) // stands in for imageDownloadTimeout
		defer cancel()
		_, err := downloadImageOutput(budget, o)
		return ImageAsset{}, err
	})
	j, _, err := s.CreateJob(context.Background(), scope, "proxied-budget", r)
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	got := awaitEnginePatiently(t, s, scope, j.ID, JobFailed)
	if got.Error == nil || got.Error.Reason != reasonDownloadTimedOut {
		t.Fatalf("error = %+v", got.Error)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("download attempts = %d, want 1: the budget was spent", n)
	}
}

// after a restart the submit's explanation is gone from the attempt, and the job's own
// restored reason is overwritten, so an interrupted job ends with a generic phrase.
func TestRegressionSubmitReasonSurvivesRestartBeforeReconcile(t *testing.T) {
	const reason = "provider did not respond within 180 seconds"
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{OwnerID: "engine-owner", BackendID: "engine-backend"}
	p, err := store.CreateProject(context.Background(), scope, "engine", "")
	if err != nil {
		t.Fatal(err)
	}
	target := SnapshotTarget(testTarget())
	target.Parameters = nil
	target.Capabilities.SupportsReconcile = false
	reg := &engineRegistry{target: target}
	a := &engineAdapter{}
	submitted := make(chan struct{}, 1)
	a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
		submitted <- struct{}{}
		return RemoteResult{State: "unknown"}, &AdapterError{Code: CodeResultUnknown, Delivery: DeliveryUncertain, Reason: reason}
	}
	options := ServiceOptions{AssetSink: &engineSink{}, AdapterResolver: func(context.Context, Scope, TargetSnapshot) (Adapter, error) { return a, nil }, PollInterval: time.Hour, MaxPollInterval: time.Hour}
	svc := NewService(store, reg, nil, options)
	sched := NewScheduler(svc)
	j, _, err := svc.CreateJob(context.Background(), scope, "restart", CreateJobRequest{ProjectID: p.ID, TargetID: target.ID, Operation: OperationGenerate, Prompt: "test", Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err = sched.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-submitted
	engineEventually(t, func() bool {
		got, e := svc.GetJob(context.Background(), scope, j.ID)
		return e == nil && got.Items[0].Error != nil && got.Items[0].Error.Reason == reason
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err = sched.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	svc2 := NewService(reopened, reg, nil, ServiceOptions{AssetSink: &engineSink{}, AdapterResolver: options.AdapterResolver, PollInterval: time.Millisecond, MaxPollInterval: 5 * time.Millisecond})
	sched2 := NewScheduler(svc2)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = sched2.Close(ctx)
	}()
	before, err := svc2.GetJob(context.Background(), scope, j.ID)
	if err != nil || before.Items[0].Error == nil || before.Items[0].Error.Reason != reason {
		t.Fatalf("sidecar did not restore the item reason: %+v %v", before.Items[0].Error, err)
	}
	// The poll backoff written before the restart is an hour; recovery only resets it for handles.
	if err = reopened.Update(WithScope(context.Background(), scope), func(tx *Transaction) error {
		ex, e := tx.ReadExecution(scope, j.ID)
		if e != nil {
			return e
		}
		x := ex.Items[before.Items[0].ID]
		x.NextPollAt = time.Time{}
		ex.Items[before.Items[0].ID] = x
		return tx.PutExecution(ex)
	}); err != nil {
		t.Fatal(err)
	}
	if err = sched2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := awaitEnginePatiently(t, svc2, scope, j.ID, JobResultUnknown)
	if got.Error == nil || got.Error.Reason != reason {
		t.Fatalf("reason after restart = %q, want %q as an uninterrupted run reports", got.Error.Reason, reason)
	}
}

// Toggle on, nothing to proxy through: the route is direct, and the toggle is already on, so the
// advice would be wrong.
func TestRegressionToggleOnWithoutProxyGetsNoAdvice(t *testing.T) {
	zz2UseProxy(t, "direct")
	zz2Direct(t, zz2Route{body: zz2Stalled})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	o := reviewSlowOutput("nil-proxy")
	o.UseProxy = true
	_, err := downloadImageOutput(ctx, o)
	if got := outputSaveError(err); got.Reason != reasonDownloadTimedOut {
		t.Fatalf("error = %+v", got)
	}
}

// A job deadline ends the download through a plain cancel, which never counts as a spent budget.
func TestRegressionPlainCancelIsNotASpentBudget(t *testing.T) {
	zz2Direct(t, zz2Route{body: zz2Stalled})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err := downloadImageOutput(ctx, reviewSlowOutput("cancelled"))
	var spent downloadBudgetSpent
	if err == nil || slowDirectDownload(err) || errors.As(err, &spent) {
		t.Fatalf("err = %v", err)
	}
}

// Job and item both carry the phrase, and both survive a reopen through the sidecar.
func TestRegressionJobAndItemCarryThePhraseAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{OwnerID: "engine-owner", BackendID: "engine-backend"}
	p, err := store.CreateProject(context.Background(), scope, "engine", "")
	if err != nil {
		t.Fatal(err)
	}
	target := SnapshotTarget(testTarget())
	target.Parameters = nil
	reg := &engineRegistry{target: target}
	a := &engineAdapter{}
	reviewOneURLOutput(a, false)
	svc := NewService(store, reg, nil, ServiceOptions{
		AssetSink: engineSinkFunc(func(context.Context, Scope, OutputSource) (ImageAsset, error) {
			return ImageAsset{}, reviewSpentSlow()
		}),
		AdapterResolver: func(context.Context, Scope, TargetSnapshot) (Adapter, error) { return a, nil },
		PollInterval:    time.Millisecond, MaxPollInterval: 5 * time.Millisecond,
	})
	sched := NewScheduler(svc)
	j, _, err := svc.CreateJob(context.Background(), scope, "both", CreateJobRequest{ProjectID: p.ID, TargetID: target.ID, Operation: OperationGenerate, Prompt: "test", Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err = sched.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	awaitEnginePatiently(t, svc, scope, j.ID, JobFailed)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err = sched.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got := reopened.data.Jobs[j.ID].Value
	if got.Error == nil || got.Error.Reason != reasonSlowDirectDownload || got.Items[0].Error == nil || got.Items[0].Error.Reason != reasonSlowDirectDownload {
		t.Fatalf("job %+v item %+v", got.Error, got.Items[0].Error)
	}
}

// adapters return state "unknown" on purpose (jimeng.go, runninghub.go, with a nil error).
// That state is recognised, and the stored reason tells the user something false about it.
func TestRegressionExplicitUnknownStateIsNotCalledUnrecognised(t *testing.T) {
	s, q, a, scope, r := engineFixture(t)
	a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
		return RemoteResult{State: "unknown", Handle: "task-1", ErrorCode: CodeNoImageOutput}, nil
	}
	j, _, err := s.CreateJob(context.Background(), scope, "explicit-unknown", r)
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	got := awaitEnginePatiently(t, s, scope, j.ID, JobResultUnknown)
	if got.Error == nil || strings.Contains(got.Error.Reason, "unrecognised") {
		t.Fatalf("reason = %q for a state the adapter set deliberately", got.Error.Reason)
	}
}
