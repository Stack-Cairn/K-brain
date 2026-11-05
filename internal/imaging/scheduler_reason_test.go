package imaging

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// awaitEnginePatiently is for tests that wait on real timers, which a loaded machine stretches.
func awaitEnginePatiently(t *testing.T, s *Service, scope Scope, id string, status JobStatus) ImageJob {
	t.Helper()
	until := time.Now().Add(60 * time.Second)
	for time.Now().Before(until) {
		j, err := s.GetJob(context.Background(), scope, id)
		if err == nil && j.Status == status {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	j, err := s.GetJob(context.Background(), scope, id)
	t.Fatalf("job %s: status %s, err %v, error %+v, want %s", id, j.Status, err, j.Error, status)
	return ImageJob{}
}

// A provider output that cannot be saved has very likely been billed, so it must not be
// reported as an unavailable module, whatever broke the save.
func TestSaveFailureReportsLostOutputWithItsCause(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		reason string
	}{
		{"timeout", &ImageError{Code: CodeUnavailable, Reason: "image download timed out"}, "image download timed out"},
		{"fake-ip", &ImageError{Code: CodeForbidden, Reason: "image host resolved to a non-public address"}, "image host resolved to a non-public address"},
		{"bare-forbidden", &ImageError{Code: CodeForbidden}, "Image access denied"},
		{"untyped", context.Canceled, "request was cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, q, _, scope, r := engineFixture(t)
			s.options.AssetSink = engineSinkFunc(func(context.Context, Scope, OutputSource) (ImageAsset, error) {
				return ImageAsset{}, tc.err
			})
			j, _, err := s.CreateJob(context.Background(), scope, "save-"+tc.name, r)
			if err != nil {
				t.Fatal(err)
			}
			q.Start(context.Background())
			got := awaitEngine(t, s, scope, j.ID, JobFailed)
			if got.Error == nil || got.Error.Code != CodeOutputSaveFailed || got.Error.Reason != tc.reason {
				t.Fatalf("error = %+v", got.Error)
			}
			raw, err := json.Marshal(got.Error)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), CodeOutputSaveFailed) {
				t.Fatalf("code rewritten on the wire: %s", raw)
			}
		})
	}
}

// A download that used its whole budget is not repeated at the same speed; a brief stall can clear
// up, and the image it would lose has already been billed. Both end with the same advice.
func TestSlowDirectDownloadRetriesOnlyABriefStall(t *testing.T) {
	stall := &ImageError{Code: CodeUnavailable, Reason: reasonSlowDirectDownload}
	for _, tc := range []struct {
		name  string
		err   error
		calls int32
	}{
		{"budget-spent", downloadBudgetSpent{stall}, 1},
		{"brief-stall", stall, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, q, _, scope, r := engineFixture(t)
			var calls atomic.Int32
			s.options.AssetSink = engineSinkFunc(func(context.Context, Scope, OutputSource) (ImageAsset, error) {
				calls.Add(1)
				return ImageAsset{}, tc.err
			})
			j, _, err := s.CreateJob(context.Background(), scope, "slow-"+tc.name, r)
			if err != nil {
				t.Fatal(err)
			}
			q.Start(context.Background())
			got := awaitEnginePatiently(t, s, scope, j.ID, JobFailed)
			if got.Error == nil || got.Error.Code != CodeOutputSaveFailed || got.Error.Reason != reasonSlowDirectDownload {
				t.Fatalf("error = %+v", got.Error)
			}
			if n := calls.Load(); n != tc.calls {
				t.Fatalf("download attempts = %d, want %d", n, tc.calls)
			}
		})
	}
}

func TestOutputSaveFailedCodeSurvivesPublicError(t *testing.T) {
	got := PublicError(&ImageError{Code: CodeOutputSaveFailed, Reason: "image host returned HTTP 403"})
	if got.Code != CodeOutputSaveFailed || got.Message != "Generated image could not be saved" {
		t.Fatalf("got %+v", got)
	}
}

// The submit timer cancels the call context, which transports report as a cancellation.
// Nobody cancelled, and the later "status unknown" step must not erase the explanation.
func TestSubmitTimeoutIsReportedAsTimeoutAndSurvivesReconcile(t *testing.T) {
	s, q, a, scope, r := engineFixture(t)
	s.options.SubmitTimeout = 2 * time.Second
	a.submit = func(ctx context.Context, _ ExecuteRequest) (RemoteResult, error) {
		<-ctx.Done()
		return RemoteResult{State: "unknown"}, &AdapterError{Code: CodeResultUnknown, Delivery: DeliveryUncertain, Reason: "no response from the provider: request was cancelled"}
	}
	j, _, err := s.CreateJob(context.Background(), scope, "submit-timeout", r)
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	got := awaitEnginePatiently(t, s, scope, j.ID, JobResultUnknown)
	if got.Error == nil || got.Error.Code != CodeResultUnknown {
		t.Fatalf("error = %+v", got.Error)
	}
	if want := "provider did not respond within 2 seconds"; got.Error.Reason != want {
		t.Fatalf("reason = %q, want %q", got.Error.Reason, want)
	}
}

func TestDownloadFailureReasonsAreFixedPhrases(t *testing.T) {
	const signed = "https://cdn.example/img.png?sig=PRIVATE_SIGNATURE"
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"deadline", context.DeadlineExceeded, "image download timed out"},
		{"dial", &ImageError{Code: CodeUnavailable, Reason: "could not connect to the image host"}, "could not connect to the image host"},
		{"url-in-error", &wrappedTransportError{url: signed}, "image download failed"},
		{"real-url-error", &url.Error{Op: "Get", URL: signed, Err: errors.New("EOF")}, "image download failed"},
	} {
		got := downloadFailureReason(tc.err)
		if got != tc.want {
			t.Errorf("%s: reason = %q, want %q", tc.name, got, tc.want)
		}
		if strings.Contains(got, "PRIVATE_SIGNATURE") {
			t.Errorf("%s: signed URL leaked: %q", tc.name, got)
		}
	}
}

type wrappedTransportError struct{ url string }

func (e *wrappedTransportError) Error() string { return "Get " + e.url + ": EOF" }
