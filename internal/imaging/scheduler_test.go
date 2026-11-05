package imaging

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestSchedulerDownloadRetryBudget(t *testing.T) {
	for _, succeed := range []bool{true, false} {
		t.Run(map[bool]string{true: "third-saves", false: "three-fail"}[succeed], func(t *testing.T) {
			s, q, _, scope, r := engineFixture(t)
			var mu sync.Mutex
			calls := 0
			var sink engineSink
			s.options.AssetSink = engineSinkFunc(func(ctx context.Context, scope Scope, output OutputSource) (ImageAsset, error) {
				mu.Lock()
				defer mu.Unlock()
				calls++
				if calls < 3 || !succeed {
					return ImageAsset{}, errors.New("download interrupted")
				}
				return sink.SaveOutput(ctx, scope, output)
			})
			j, _, err := s.CreateJob(context.Background(), scope, "download", r)
			if err != nil {
				t.Fatal(err)
			}
			q.Start(context.Background())
			want := JobFailed
			if succeed {
				want = JobSucceeded
			}
			awaitEngine(t, s, scope, j.ID, want)
			mu.Lock()
			count := calls
			mu.Unlock()
			if count != 3 {
				t.Fatal(count)
			}
			ex, _ := s.store.ReadExecution(scope, j.ID)
			if ex.Items[j.Items[0].ID].DownloadAttempts != 3 {
				t.Fatal(ex)
			}
		})
	}
}

func TestSchedulerConcurrencyFairnessAndPendingQuota(t *testing.T) {
	s, q, a, scope, r := engineFixture(t)
	s.options.GlobalConcurrency = 2
	s.options.ConnectionConcurrency = func(Scope, string) int { return 1 }
	var mu sync.Mutex
	running, maxRunning := 0, 0
	byConnection := map[string]int{}
	order := make(chan string, 8)
	release := make(chan struct{}, 8)
	a.submit = func(ctx context.Context, req ExecuteRequest) (RemoteResult, error) {
		id := req.Target.ConnectionID
		mu.Lock()
		running++
		byConnection[id]++
		if running > maxRunning {
			maxRunning = running
		}
		if byConnection[id] > 1 {
			t.Error("connection concurrency exceeded")
		}
		mu.Unlock()
		order <- id
		select {
		case <-ctx.Done():
		case <-release:
		}
		mu.Lock()
		running--
		byConnection[id]--
		mu.Unlock()
		return RemoteResult{State: "pending", Handle: req.ItemID, NextPollAfter: time.Hour}, nil
	}
	reg := s.registry.(*engineRegistry)
	reg.target.ConnectionID = "ic_a"
	r.Count = 3
	first, _, err := s.CreateJob(context.Background(), scope, "a", r)
	if err != nil {
		t.Fatal(err)
	}
	reg.target.ConnectionID = "ic_b"
	second, _, err := s.CreateJob(context.Background(), scope, "b", r)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case id := <-order:
			got[id] = true
		case <-time.After(time.Second):
			t.Fatal("unfair or blocked scheduler")
		}
	}
	if len(got) != 2 {
		t.Fatal(got)
	}
	release <- struct{}{}
	release <- struct{}{}
	engineEventually(t, func() bool {
		ex1, _ := s.store.ReadExecution(scope, first.ID)
		ex2, _ := s.store.ReadExecution(scope, second.ID)
		count := 0
		for _, ex := range []ExecutionRecord{ex1, ex2} {
			for _, x := range ex.Items {
				if x.ProviderHandle != "" {
					count++
				}
			}
		}
		return count == 2
	})
	s.notify()
	select {
	case <-order:
		t.Fatal("pending tasks did not retain connection quota")
	case <-time.After(20 * time.Millisecond):
	}
	mu.Lock()
	peak := maxRunning
	mu.Unlock()
	if peak != 2 {
		t.Fatal(peak)
	}
}
func TestSchedulerClockDeadlineAndLease(t *testing.T) {
	s, q, a, scope, r := engineFixture(t)
	clock := &engineClock{now: time.Now().UTC()}
	s.clock = clock
	s.options.JobTimeout = time.Minute
	s.options.SubmitTimeout = time.Hour
	s.options.LeaseDuration = 30 * time.Second
	s.options.LeaseRenewInterval = 10 * time.Second
	entered := make(chan struct{})
	a.submit = func(ctx context.Context, _ ExecuteRequest) (RemoteResult, error) {
		close(entered)
		<-ctx.Done()
		return RemoteResult{}, ctx.Err()
	}
	j, _, err := s.CreateJob(context.Background(), scope, "clock", r)
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	<-entered
	ex, _ := s.store.ReadExecution(scope, j.ID)
	old := ex.Items[j.Items[0].ID].LeaseExpiresAt
	clock.advance(11 * time.Second)
	s.notify()
	engineEventually(t, func() bool {
		ex, _ := s.store.ReadExecution(scope, j.ID)
		return ex.Items[j.Items[0].ID].LeaseExpiresAt.After(old)
	})
	clock.advance(time.Minute)
	s.notify()
	awaitEngine(t, s, scope, j.ID, JobResultUnknown)
	sub, _, _, _ := a.counts()
	if sub != 1 {
		t.Fatal(sub)
	}
}
func TestSchedulerQueryFailureBudgetAndHandle(t *testing.T) {
	s, q, a, scope, r := engineFixture(t)
	a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
		return RemoteResult{State: "pending", Handle: "private-handle"}, nil
	}
	a.poll = func(context.Context, string) (RemoteResult, error) {
		return RemoteResult{}, errors.New("secret upstream body")
	}
	j, _, err := s.CreateJob(context.Background(), scope, "query", r)
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	awaitEngine(t, s, scope, j.ID, JobResultUnknown)
	sub, polls, _, _ := a.counts()
	if sub != 1 || polls != 5 {
		t.Fatal(sub, polls)
	}
	ex, _ := s.store.ReadExecution(scope, j.ID)
	if ex.Items[j.Items[0].ID].ProviderHandle != "private-handle" {
		t.Fatal(ex)
	}
}
func TestSchedulerRealOutputAncestryAndUnsupportedURL(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(map[bool]string{false: "bytes", true: "private-url-rejected"}[remote], func(t *testing.T) {
			s, q, a, scope, r := engineFixture(t)
			assets, err := NewAssetStore(s.store, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer assets.Close()
			s.options.AssetSink = assets
			s.options.InputResolver = assets
			raw := imageTestBytes(t, "png", 3, 2)
			input, err := assets.ImportAsset(context.Background(), scope, bytes.NewReader(raw), "image/png")
			if err != nil {
				t.Fatal(err)
			}
			reg := s.registry.(*engineRegistry)
			reg.target.Capabilities.Operations = []Operation{OperationGenerate, OperationReference}
			reg.target.Capabilities.MaxInputs = 2
			r.Operation = OperationReference
			r.Inputs = []InputAsset{{AssetID: input.ID, Role: "reference"}}
			r.ParentAssetID = input.ID
			a.submit = func(_ context.Context, req ExecuteRequest) (RemoteResult, error) {
				if req.Request.Count != 1 || len(req.Inputs) != 1 {
					t.Error("invalid resolved request")
				}
				output := OutputSource{Kind: "bytes", Data: raw, MIME: "image/png"}
				if remote {
					output = OutputSource{Kind: "url", URL: "https://127.0.0.1/image.png"}
				}
				return RemoteResult{State: "completed", Outputs: []OutputSource{output}}, nil
			}
			j, _, err := s.CreateJob(context.Background(), scope, "real", r)
			if err != nil {
				t.Fatal(err)
			}
			q.Start(context.Background())
			if remote {
				j = awaitEngine(t, s, scope, j.ID, JobFailed)
				// Still refused and never fetched; the job reports the output as lost and says why.
				if j.Error.Code != CodeOutputSaveFailed || j.Error.Reason != "provider returned an image address that is not allowed" {
					t.Fatal(j.Error)
				}
				return
			}
			j = awaitEngine(t, s, scope, j.ID, JobSucceeded)
			reader, asset, err := assets.OpenAsset(context.Background(), scope, j.AssetIDs[0], "original")
			if err != nil {
				t.Fatal(err)
			}
			actual, _ := io.ReadAll(reader)
			reader.Close()
			if !bytes.Equal(raw, actual) || asset.JobID != j.ID || len(asset.ParentAssetIDs) != 1 || asset.ParentAssetIDs[0] != input.ID {
				t.Fatal(asset)
			}
			events, _ := s.ReadEvents(context.Background(), scope, j.ID, 0, 100)
			ready := 0
			for _, event := range events {
				if event.Type == EventAssetReady {
					ready++
					if event.Data.Asset.ID != asset.ID {
						t.Fatal(event)
					}
				}
			}
			if ready != 1 {
				t.Fatal(ready)
			}
		})
	}
}
func TestSchedulerUsageExactAndUnknown(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		s, q, a, scope, r := engineFixture(t)
		r.Count = 2
		var mu sync.Mutex
		calls := 0
		a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			amount := "0.1"
			u := &Usage{Amount: &amount, Currency: "USD", Source: "provider"}
			if unknown && calls == 2 {
				u = nil
			}
			return RemoteResult{State: "completed", Usage: u, Outputs: []OutputSource{{Kind: "bytes", Data: []byte("image")}}}, nil
		}
		j, _, err := s.CreateJob(context.Background(), scope, "usage", r)
		if err != nil {
			t.Fatal(err)
		}
		q.Start(context.Background())
		j = awaitEngine(t, s, scope, j.ID, JobSucceeded)
		if unknown {
			if j.Usage != nil {
				t.Fatal(j.Usage)
			}
		} else if j.Usage == nil || *j.Usage.Amount != "0.2" {
			t.Fatal(j.Usage)
		}
	}
}

func TestCancelPendingConfirmationAndTimeout(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		t.Run(map[bool]string{true: "confirmed", false: "timeout"}[confirmed], func(t *testing.T) {
			s, q, a, scope, r := engineFixture(t)
			s.registry.(*engineRegistry).target.Capabilities.SupportsCancel = true
			a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
				return RemoteResult{State: "pending", Handle: "cancel-handle", NextPollAfter: time.Hour}, nil
			}
			a.cancel = func(context.Context, string) error {
				if confirmed {
					return nil
				}
				return context.DeadlineExceeded
			}
			j, _, err := s.CreateJob(context.Background(), scope, "cancel-pending", r)
			if err != nil {
				t.Fatal(err)
			}
			q.Start(context.Background())
			engineEventually(t, func() bool {
				ex, _ := s.store.ReadExecution(scope, j.ID)
				return ex.Items[j.Items[0].ID].ProviderHandle != ""
			})
			if _, err = s.CancelJob(context.Background(), scope, j.ID); err != nil {
				t.Fatal(err)
			}
			j = awaitEngine(t, s, scope, j.ID, JobCancelled)
			effect := "local_only"
			if confirmed {
				effect = "upstream_confirmed"
			}
			if j.CancelEffect != effect {
				t.Fatal(j)
			}
			s.CancelJob(context.Background(), scope, j.ID)
			sub, _, _, cancels := a.counts()
			if sub != 1 || cancels != 1 {
				t.Fatal(sub, cancels)
			}
			events, _ := s.ReadEvents(context.Background(), scope, j.ID, 0, 100)
			requested := false
			for _, e := range events {
				if e.Data.Status == JobCancelRequested {
					requested = true
				}
			}
			if !requested {
				t.Fatal("cancel request was not durable")
			}
		})
	}
}
func TestSchedulerNoAdapterCannotSucceed(t *testing.T) {
	s, q, _, scope, r := engineFixture(t)
	s.options.AdapterResolver = nil
	j, _, err := s.CreateJob(context.Background(), scope, "none", r)
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	j = awaitEngine(t, s, scope, j.ID, JobFailed)
	if j.Error.Code != CodeUnsupportedOperation || len(j.AssetIDs) != 0 {
		t.Fatal(j)
	}
}
func TestSchedulerRejectsSecondEngine(t *testing.T) {
	s, q, _, _, _ := engineFixture(t)
	if err := q.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := NewScheduler(NewService(s.store, s.registry, nil, s.options))
	if err := second.Start(context.Background()); err == nil {
		second.Close(context.Background())
		t.Fatal("second engine acquired store")
	}
	second.Close(context.Background())
}

func TestSchedulerPartialAndNoOutput(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "Partial", true: "Empty"}[empty], func(t *testing.T) {
			s, q, a, scope, r := engineFixture(t)
			s.options.AssetSink = &engineSink{fail: true}
			a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
				out := []OutputSource{{Kind: "bytes", Data: []byte("good")}, {Kind: "bytes", Data: []byte("bad")}}
				if empty {
					out = nil
				}
				return RemoteResult{State: "completed", Outputs: out}, nil
			}
			j, _, err := s.CreateJob(context.Background(), scope, "key", r)
			if err != nil {
				t.Fatal(err)
			}
			q.Start(context.Background())
			want := JobPartialSucceeded
			if empty {
				want = JobFailed
			}
			j = awaitEngine(t, s, scope, j.ID, want)
			if empty {
				if len(j.AssetIDs) != 0 || j.Error.Code != CodeNoImageOutput {
					t.Fatal(j)
				}
			} else {
				if len(j.AssetIDs) != 1 {
					t.Fatal(j)
				}
				asset, err := s.store.ReadAsset(scope, j.AssetIDs[0])
				if err != nil || asset.JobID != j.ID {
					t.Fatal(asset, err)
				}
			}
		})
	}
}
func TestSchedulerRetryDelivery(t *testing.T) {
	for _, delivery := range []Delivery{DeliveryNotSent, DeliveryRejected, DeliveryUncertain} {
		t.Run(string(delivery), func(t *testing.T) {
			s, q, a, scope, r := engineFixture(t)
			a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
				return RemoteResult{}, &AdapterError{Code: CodeRateLimited, Delivery: delivery}
			}
			j, _, _ := s.CreateJob(context.Background(), scope, "key", r)
			q.Start(context.Background())
			want := JobFailed
			if delivery == DeliveryUncertain {
				want = JobResultUnknown
			}
			awaitEngine(t, s, scope, j.ID, want)
			sub, _, rec, _ := a.counts()
			expected := 1
			if delivery == DeliveryNotSent {
				expected = 3
			}
			if sub != expected {
				t.Fatalf("submits=%d", sub)
			}
			if delivery == DeliveryUncertain && rec != 1 {
				t.Fatalf("reconciles=%d", rec)
			}
		})
	}
}
func TestCancelTerminalRace(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		t.Run(map[bool]string{true: "cancel-first", false: "complete-first"}[cancelFirst], func(t *testing.T) {
			s, q, a, scope, r := engineFixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
				close(entered)
				<-release
				return RemoteResult{State: "completed", Outputs: []OutputSource{{Kind: "bytes", Data: []byte("image")}}}, nil
			}
			j, _, _ := s.CreateJob(context.Background(), scope, "key", r)
			q.Start(context.Background())
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("no submit")
			}
			if cancelFirst {
				if _, err := s.CancelJob(context.Background(), scope, j.ID); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			if !cancelFirst {
				awaitEngine(t, s, scope, j.ID, JobSucceeded)
				s.CancelJob(context.Background(), scope, j.ID)
			}
			want := JobSucceeded
			if cancelFirst {
				want = JobCancelled
			}
			until := time.Now().Add(time.Second)
			for time.Now().Before(until) {
				j, _ = s.GetJob(context.Background(), scope, j.ID)
				if len(j.AssetIDs) == 1 {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if j.Status != want || len(j.AssetIDs) != 1 {
				t.Fatal(j)
			}
			again, _ := s.CancelJob(context.Background(), scope, j.ID)
			if again.Status != want {
				t.Fatal(again)
			}
			sub, _, _, _ := a.counts()
			if sub != 1 {
				t.Fatal(sub)
			}
		})
	}
}
func TestCancelQueuedDoesNotSubmit(t *testing.T) {
	s, q, a, scope, r := engineFixture(t)
	j, _, _ := s.CreateJob(context.Background(), scope, "key", r)
	j, err := s.CancelJob(context.Background(), scope, j.ID)
	if err != nil || j.Status != JobCancelled || j.CancelEffect != "not_submitted" {
		t.Fatal(j, err)
	}
	q.Start(context.Background())
	q.Close(context.Background())
	sub, _, _, _ := a.counts()
	if sub != 0 {
		t.Fatal(sub)
	}
}
