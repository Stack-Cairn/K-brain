package imaging

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Independent review. Every test in this file fails on the reviewed working tree and
// states the behaviour that should hold once the defect is fixed.

func zzFixture(t *testing.T, dir string) (*Service, *Scheduler, *engineAdapter, Scope, CreateJobRequest, *Store) {
	t.Helper()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	scope := Scope{OwnerID: "engine-owner", BackendID: "engine-backend"}
	p, err := store.CreateProject(context.Background(), scope, "engine", "")
	if err != nil {
		t.Fatal(err)
	}
	target := SnapshotTarget(testTarget())
	target.Parameters = nil
	target.Capabilities.MaxCount = 16
	target.Capabilities.SupportsReconcile = true
	a := &engineAdapter{}
	svc := NewService(store, &engineRegistry{target: target}, nil, ServiceOptions{AssetSink: &engineSink{}, AdapterResolver: func(context.Context, Scope, TargetSnapshot) (Adapter, error) { return a, nil }, PollInterval: time.Millisecond, MaxPollInterval: 5 * time.Millisecond})
	q := NewScheduler(svc)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		q.Close(ctx)
	})
	return svc, q, a, scope, CreateJobRequest{ProjectID: p.ID, TargetID: target.ID, Operation: OperationGenerate, Prompt: "test", Count: 1}, store
}

// zzLegacyImageError is ImageError exactly as shipped at 6f960aa, which has no UnmarshalJSON.
type zzLegacyImageError struct {
	Code      string        `json:"code"`
	Message   string        `json:"message"`
	RequestID string        `json:"requestId"`
	Retryable bool          `json:"retryable"`
	Details   *ErrorDetails `json:"details,omitempty"`
}

func zzVisitErrors(v any, visit func(raw []byte)) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if m, ok := child.(map[string]any); ok && k == "error" {
				raw, _ := json.Marshal(m)
				visit(raw)
			}
			zzVisitErrors(child, visit)
		}
	case []any:
		for _, child := range x {
			zzVisitErrors(child, visit)
		}
	}
}

// docs/compatibility.md 6.2 names the image WAL: an old binary must keep reading and writing
// G0. The WAL and snapshot are decoded with DisallowUnknownFields, including nested errors.
// With ZZ_NEW_STORE_OUT set, the directories are kept for the real old-binary check.
func TestReasonRegressionPersistedErrorStaysReadableByPreviousSchema(t *testing.T) {
	dir := t.TempDir()
	if out := os.Getenv("ZZ_NEW_STORE_OUT"); out != "" {
		dir = filepath.Join(out, "reason")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s, q, a, scope, r, store := zzFixture(t, dir)
	a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
		return RemoteResult{State: "failed", ErrorCode: CodeAuthRequired}, &AdapterError{Code: CodeAuthRequired, Delivery: DeliveryRejected, Reason: "provider returned HTTP 401"}
	}
	j, _, err := s.CreateJob(context.Background(), scope, "compat", r)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	awaitEngine(t, s, scope, j.ID, JobFailed)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err = q.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	checked, rejected := 0, ""
	if err = filepath.WalkDir(dir, func(path string, e fs.DirEntry, walkErr error) error {
		if walkErr != nil || e.IsDir() {
			return walkErr
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range bytes.Split(raw, []byte{'\n'}) {
			rec, err := decodeRecord(line)
			if err != nil {
				continue
			}
			var payload any
			if err = json.Unmarshal(rec.Payload, &payload); err != nil {
				continue
			}
			zzVisitErrors(payload, func(raw []byte) {
				checked++
				var legacy zzLegacyImageError
				if err := decodeStrict(raw, &legacy); err != nil && rejected == "" {
					rejected = err.Error() + " in " + string(raw)
				}
			})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("found no persisted error objects")
	}
	if rejected != "" {
		t.Fatalf("previous K-brain would report %s opening this store: %s", CodeStoreCorrupt, rejected)
	}
}

// The new binary must read a store written before the field existed. Env-gated because the
// fixture is produced by the 6f960aa tree; see the review report for the commands.
func TestReasonRegressionNewBinaryReadsStoreWrittenByPreviousVersion(t *testing.T) {
	dir := os.Getenv("ZZ_OLD_STORE_IN")
	if dir == "" {
		t.Skip("ZZ_OLD_STORE_IN not set")
	}
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scope := Scope{OwnerID: "engine-owner", BackendID: "engine-backend"}
	jobs := 0
	store.mu.RLock()
	for _, r := range store.data.Jobs {
		jobs++
		if r.Value.Error == nil || r.Value.Error.Reason != "" || r.Scope.OwnerID != scope.OwnerID {
			t.Errorf("job = %+v", r.Value)
		}
	}
	store.mu.RUnlock()
	if jobs == 0 {
		t.Fatal("old store has no jobs")
	}
}

// The rewrite only checks that the call context has ended. It must not replace an answer the
// provider did give, or claim silence about a request that never left the process.
func TestReasonRegressionTimerDoesNotOverwriteUnrelatedAdapterError(t *testing.T) {
	for _, tc := range []struct {
		name     string
		code     string
		delivery Delivery
		reason   string
	}{
		{"http_401_body_read_interrupted", CodeAuthRequired, DeliveryRejected, "provider returned HTTP 401"},
		{"not_sent", CodeUnavailable, DeliveryNotSent, "request was cancelled or timed out before it was sent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, q, a, scope, r := engineFixture(t)
			s.options.SubmitTimeout = time.Second
			a.submit = func(ctx context.Context, _ ExecuteRequest) (RemoteResult, error) {
				<-ctx.Done()
				return RemoteResult{State: "failed", ErrorCode: tc.code}, &AdapterError{Code: tc.code, Delivery: tc.delivery, Reason: tc.reason}
			}
			j, _, err := s.CreateJob(context.Background(), scope, "timer-"+tc.name, r)
			if err != nil {
				t.Fatal(err)
			}
			q.Start(context.Background())
			got := awaitEnginePatiently(t, s, scope, j.ID, JobFailed)
			if got.Error == nil || got.Error.Code != tc.code {
				t.Fatalf("error = %+v", got.Error)
			}
			if got.Error.Reason != tc.reason {
				t.Fatalf("reason = %q, want %q", got.Error.Reason, tc.reason)
			}
		})
	}
}

type zzRoundTrip func(*http.Request) (*http.Response, error)

func (f zzRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// finish wraps the deadline with timedContext, whose timer calls cancel(), so ctx.Err() is
// context.Canceled. Same misreport the submit path fixed: nobody cancelled this job.
func TestReasonRegressionSaveInterruptedByJobDeadlineIsNotReportedAsCancelled(t *testing.T) {
	s, q, a, scope, r := engineFixture(t)
	s.options.JobTimeout = 2 * time.Second
	a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
		return RemoteResult{State: "completed", Outputs: []OutputSource{{Kind: "url", URL: "https://cdn.example.com/a.png"}}}, nil
	}
	hanging := zzRoundTrip(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	s.options.AssetSink = engineSinkFunc(func(ctx context.Context, _ Scope, o OutputSource) (ImageAsset, error) {
		_, err := retrieveImageOutput(ctx, o, hanging)
		return ImageAsset{}, err
	})
	j, _, err := s.CreateJob(context.Background(), scope, "deadline-save", r)
	if err != nil {
		t.Fatal(err)
	}
	q.Start(context.Background())
	got := awaitEnginePatiently(t, s, scope, j.ID, JobFailed)
	if got.Error == nil || got.Error.Code != CodeOutputSaveFailed {
		t.Fatalf("error = %+v", got.Error)
	}
	if strings.Contains(got.Error.Reason, "cancel") {
		t.Fatalf("job deadline reported as a cancellation: %q", got.Error.Reason)
	}
}

// After five failed polls of a successfully submitted task, the item must keep the reason of
// the failure that ended it. The submit recorded none, so lastAttemptReason hides it.
func TestReasonRegressionQueryExhaustionKeepsTheLookupReason(t *testing.T) {
	const lookup = "provider returned HTTP 503"
	lookupFailure := func() error {
		return &AdapterError{Code: CodeResultUnknown, Delivery: DeliveryUncertain, Reason: lookup}
	}
	t.Run("poll_reason_is_discarded", func(t *testing.T) {
		s, q, a, scope, r := engineFixture(t)
		a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
			return RemoteResult{State: "pending", Handle: "task-1"}, nil
		}
		a.poll = func(context.Context, string) (RemoteResult, error) {
			return RemoteResult{State: "unknown"}, lookupFailure()
		}
		j, _, err := s.CreateJob(context.Background(), scope, "poll-exhaust", r)
		if err != nil {
			t.Fatal(err)
		}
		q.Start(context.Background())
		got := awaitEngine(t, s, scope, j.ID, JobResultUnknown)
		if got.Error == nil {
			t.Fatal("no error")
		}
		if got.Error.Reason != lookup {
			t.Fatalf("reason = %q, want %q", got.Error.Reason, lookup)
		}
	})
}

// Secrets split by an invisible character must not survive because the character became a space.
func TestReasonRegressionSafeReasonFormatCharacterCannotSplitASecret(t *testing.T) {
	for in, secret := range map[string]string{
		"key sk-​ABCDEFGHIJKL rejected":                      "ABCDEFGHIJKL",
		"fetch https://cdn​.example.com/a?sig=abc123def bad": "abc123def",
	} {
		got := SafeReason(in)
		if strings.Contains(got, secret) {
			t.Errorf("%q leaked %q in %q", in, secret, got)
		}
	}
}
