package imaging

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// Independent review. These attacks did not find a defect; they pin the behaviour that was
// checked and pass on the reviewed working tree.

func TestReasonVerifiedSafeReasonBoundsAndIdempotence(t *testing.T) {
	inputs := []string{
		strings.Repeat("字", 400),
		strings.Repeat("👩‍💻", 200),
		strings.Repeat("a b ", 5000),
		strings.Repeat("x", 200000),
		"bad\xff\xfe" + strings.Repeat("é", 300),
		"https://a.example/" + strings.Repeat("q", 1000) + " tail",
		"line1\r\nline2‮⁦\x00end",
		"bearer https://a.example/x and me@example.com at 10.0.0.1:80 /a/b sk-abcdefgh",
		strings.Repeat("sk-abcdefgh ", 100),
	}
	for _, in := range inputs {
		once := SafeReason(in)
		if !utf8.ValidString(once) || utf8.RuneCountInString(once) > maxReasonRunes {
			t.Errorf("not bounded valid UTF-8: %d runes", utf8.RuneCountInString(once))
		}
		if strings.ContainsAny(once, "\r\n\x00‮⁦") {
			t.Errorf("control or format character survived: %q", once)
		}
		if twice := SafeReason(once); twice != once {
			t.Errorf("not idempotent:\n%q\n%q", once, twice)
		}
	}
}

// Replay and restart must return the stored reason unchanged.
func TestReasonVerifiedReasonStableAcrossReopenAndIdempotentReplay(t *testing.T) {
	dir := t.TempDir()
	s, q, a, scope, r, store := zzFixture(t, dir)
	const reason = "provider returned HTTP 422: size not supported"
	a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
		return RemoteResult{State: "failed", ErrorCode: CodeInvalidInput}, &AdapterError{Code: CodeInvalidInput, Delivery: DeliveryRejected, Reason: reason}
	}
	j, _, err := s.CreateJob(context.Background(), scope, "stable", r)
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	got := awaitEngine(t, s, scope, j.ID, JobFailed)
	if got.Error.Reason != reason || got.Items[0].Error.Reason != reason {
		t.Fatalf("job %+v item %+v", got.Error, got.Items[0].Error)
	}
	replay, replayed, err := s.CreateJob(context.Background(), scope, "stable", r)
	if err != nil || !replayed && replay.ID != j.ID {
		t.Fatalf("replay %v %v", replayed, err)
	}
	if replay.ID != j.ID || replay.Error == nil || replay.Error.Reason != reason {
		t.Fatalf("replay = %+v", replay.Error)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err = q.Close(ctx); err != nil {
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
	reopened.mu.RLock()
	stored := reopened.data.Jobs[j.ID].Value.Error
	exec := reopened.data.Executions[j.ID]
	reopened.mu.RUnlock()
	if stored == nil || stored.Reason != reason {
		t.Fatalf("reopened = %+v", stored)
	}
	// Attempt history keeps its code but not its reason across a restart: the sidecar only
	// holds what the job and its items show, and a missing reason just loses a hint.
	attempts := exec.Items[got.Items[0].ID].Attempts
	if len(attempts) != 1 || attempts[0].Error == nil || attempts[0].Error.Code != CodeInvalidInput {
		t.Fatalf("attempt history = %+v", attempts)
	}
}

func TestReasonVerifiedCancelDuringSubmitIsNotRewritten(t *testing.T) {
	s, q, a, scope, r := engineFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
		close(entered)
		<-release
		return RemoteResult{State: "unknown"}, &AdapterError{Code: CodeResultUnknown, Delivery: DeliveryUncertain, Reason: "no response"}
	}
	j, _, err := s.CreateJob(context.Background(), scope, "cancel", r)
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	<-entered
	if _, err = s.CancelJob(context.Background(), scope, j.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	got := awaitEngine(t, s, scope, j.ID, JobCancelled)
	if got.Error != nil {
		t.Fatalf("cancelled job carries %+v", got.Error)
	}
}

// Shutdown while submitting or saving stays recoverable and writes no failure.
func TestReasonVerifiedShutdownLeavesNoFailureRecord(t *testing.T) {
	for _, stage := range []string{"submit", "save"} {
		t.Run(stage, func(t *testing.T) {
			s, q, a, scope, r := engineFixture(t)
			entered := make(chan struct{})
			if stage == "submit" {
				a.submit = func(ctx context.Context, _ ExecuteRequest) (RemoteResult, error) {
					close(entered)
					<-ctx.Done()
					return RemoteResult{State: "unknown"}, &AdapterError{Code: CodeResultUnknown, Delivery: DeliveryUncertain, Reason: "no response"}
				}
			} else {
				s.options.AssetSink = engineSinkFunc(func(ctx context.Context, _ Scope, _ OutputSource) (ImageAsset, error) {
					close(entered)
					<-ctx.Done()
					return ImageAsset{}, ctx.Err()
				})
			}
			j, _, err := s.CreateJob(context.Background(), scope, "shutdown-"+stage, r)
			if err != nil {
				t.Fatal(err)
			}
			q.Start(context.Background())
			<-entered
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err = q.Close(ctx); err != nil {
				t.Fatal(err)
			}
			got, err := s.GetJob(context.Background(), scope, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if terminalJob(got.Status) || got.Error != nil || got.Items[0].Error != nil {
				t.Fatalf("status %s job %+v item %+v", got.Status, got.Error, got.Items[0].Error)
			}
		})
	}
}

// A job deadline during submit settles on the deadline reason, not on a "0 seconds" message.
func TestReasonVerifiedSubmitCutByJobDeadlineSettlesOnDeadlineReason(t *testing.T) {
	s, q, a, scope, r := engineFixture(t)
	s.options.JobTimeout = 2 * time.Second
	a.submit = func(ctx context.Context, _ ExecuteRequest) (RemoteResult, error) {
		<-ctx.Done()
		return RemoteResult{State: "unknown"}, &AdapterError{Code: CodeResultUnknown, Delivery: DeliveryUncertain, Reason: "no response"}
	}
	j, _, err := s.CreateJob(context.Background(), scope, "deadline-submit", r)
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	got := awaitEnginePatiently(t, s, scope, j.ID, JobResultUnknown)
	if got.Error == nil || got.Error.Reason != "job deadline passed before the provider reported a result" {
		t.Fatalf("error = %+v", got.Error)
	}
}

func TestReasonVerifiedPartialSaveKeepsAssetAndCountsDownloadAttempts(t *testing.T) {
	s, q, a, scope, r := engineFixture(t)
	a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
		return RemoteResult{State: "completed", Outputs: []OutputSource{{Kind: "bytes", Data: []byte("good")}, {Kind: "bytes", Data: []byte("bad")}}}, nil
	}
	var calls atomic.Int32
	s.options.AssetSink = engineSinkFunc(func(_ context.Context, _ Scope, o OutputSource) (ImageAsset, error) {
		calls.Add(1)
		if string(o.Data) == "bad" {
			return ImageAsset{}, &ImageError{Code: CodeForbidden, Reason: "image host resolved to a non-public address"}
		}
		return ImageAsset{ID: engineID("ia_"), Hash: "h", MIME: "image/png", Bytes: 4, Width: 1, Height: 1, Available: true, CreatedAt: time.Now()}, nil
	})
	j, _, err := s.CreateJob(context.Background(), scope, "partial", r)
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	got := awaitEngine(t, s, scope, j.ID, JobPartialSucceeded)
	if got.Error == nil || got.Error.Code != CodeOutputSaveFailed || got.Error.Reason != "image host resolved to a non-public address" || len(got.AssetIDs) != 1 {
		t.Fatalf("job = %+v assets %v", got.Error, got.AssetIDs)
	}
	if calls.Load() != 4 {
		t.Fatalf("sink calls = %d, want 1 good + 3 bad", calls.Load())
	}
	s.store.mu.RLock()
	x := s.store.data.Executions[j.ID].Items[got.Items[0].ID]
	s.store.mu.RUnlock()
	if x.DownloadAttempts != 4 {
		t.Fatalf("download attempts = %d", x.DownloadAttempts)
	}
}

func TestReasonVerifiedDownloadErrorsNeverCarrySignedURL(t *testing.T) {
	const signed = "https://cdn.example.com/a.png?sig=PRIVATE_SIGNATURE"
	source := OutputSource{Kind: "url", URL: signed}
	for _, tc := range []struct {
		name string
		rt   zzRoundTrip
		want string
	}{
		{"transport", func(*http.Request) (*http.Response, error) {
			return nil, &url.Error{Op: "Get", URL: signed, Err: errors.New("EOF " + signed)}
		}, "image download failed"},
		{"status", func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("denied " + signed)), Header: http.Header{}, Request: req}, nil
		}, "image host returned HTTP 403"},
	} {
		_, err := retrieveImageOutput(context.Background(), source, tc.rt)
		got := outputSaveError(err)
		if got.Code != CodeOutputSaveFailed || got.Reason != tc.want || strings.Contains(got.Reason, "PRIVATE") || strings.Contains(got.Reason, "cdn.example") {
			t.Errorf("%s: code %s reason %q", tc.name, got.Code, got.Reason)
		}
	}
}

// The designed use of lastAttemptReason: exhausted reconcile keeps the submit's explanation.
// This also means five later HTTP 503 lookups are not mentioned in the final reason.
func TestReasonVerifiedReconcileExhaustionKeepsSubmitReason(t *testing.T) {
	s, q, a, scope, r := engineFixture(t)
	const submit = "no response from the provider: request timed out"
	a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
		return RemoteResult{State: "unknown"}, &AdapterError{Code: CodeResultUnknown, Delivery: DeliveryUncertain, Reason: submit}
	}
	a.reconcile = func(context.Context, string) (RemoteResult, error) {
		return RemoteResult{State: "unknown"}, &AdapterError{Code: CodeResultUnknown, Delivery: DeliveryUncertain, Reason: "provider returned HTTP 503"}
	}
	j, _, err := s.CreateJob(context.Background(), scope, "reconcile-exhaust", r)
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	got := awaitEngine(t, s, scope, j.ID, JobResultUnknown)
	if got.Error == nil || got.Error.Reason != submit {
		t.Fatalf("reason = %+v", got.Error)
	}
}
