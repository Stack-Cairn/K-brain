package imaging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const zz2Error = `{"code":"IMAGE_UNAVAILABLE","message":"m","requestId":"","retryable":false,"reason":"drop me"}`

// Everything except the matching reason survives byte-exact in value, and the record verifies.
func TestRegressionStripKeepsValuesAndChecksumOrder(t *testing.T) {
	payload := map[string]any{
		"big":   json.Number("9007199254740993"),
		"huge":  json.Number("123456789012345678901234567890"),
		"float": json.Number("1.10"),
		"exp":   json.Number("1e400"),
		"html":  `<a&b> ` + " ",
		"text":  `say "reason" here`,
		"nest":  []any{json.RawMessage(zz2Error)},
	}
	line, err := encodeRecord(7, payload)
	if err != nil {
		t.Fatal(err)
	}
	r, err := decodeRecord(bytes.TrimRight(line, "\n"))
	if err != nil {
		t.Fatalf("record does not verify: %v", err)
	}
	if bytes.Contains(r.Payload, []byte("drop me")) {
		t.Fatalf("reason survived: %s", r.Payload)
	}
	var out map[string]any
	d := json.NewDecoder(bytes.NewReader(r.Payload))
	d.UseNumber()
	if err := d.Decode(&out); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"big", "huge", "float", "exp", "html", "text"} {
		if fmt.Sprint(out[key]) != fmt.Sprint(payload[key]) {
			t.Errorf("%s: %v != %v", key, out[key], payload[key])
		}
	}
}

// Near misses of the shape keep their field: stripping is not done by field name alone.
func TestRegressionStripLeavesLookalikesAlone(t *testing.T) {
	for _, raw := range []string{
		`{"reason":"keep"}`,
		`{"code":"x","reason":"keep"}`,
		`{"code":"x","message":"m","requestId":"r","reason":"keep"}`,
		`{"code":"x","message":"m","requestId":"r","retryable":"no","reason":"keep"}`,
		`{"code":1,"message":"m","requestId":"r","retryable":true,"reason":"keep"}`,
	} {
		got, err := stripErrorReasons([]byte(raw))
		if err != nil || !bytes.Contains(got, []byte("keep")) {
			t.Errorf("%s -> %s, %v", raw, got, err)
		}
	}
}

func zz2FailedJob(t *testing.T, dir string, count int) (*Store, Scope, []string, *Service, *Scheduler) {
	t.Helper()
	s, q, a, scope, r, store := zzFixture(t, dir)
	a.submit = func(context.Context, ExecuteRequest) (RemoteResult, error) {
		return RemoteResult{State: "failed", ErrorCode: CodeInvalidInput}, &AdapterError{Code: CodeInvalidInput, Delivery: DeliveryRejected, Reason: "provider returned HTTP 422"}
	}
	q.Start(context.Background())
	var ids []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			j, _, err := s.CreateJob(context.Background(), scope, fmt.Sprintf("zz2-%d", i), r)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			ids = append(ids, j.ID)
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	for _, id := range ids {
		awaitEngine(t, s, scope, id, JobFailed)
	}
	return store, scope, ids, s, q
}

func zz2CloseFixture(t *testing.T, store *Store, q *Scheduler) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := q.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

// Concurrent transactions leave a parseable sidecar that restores every job. Meant for -race.
func TestRegressionConcurrentFailuresKeepSidecarConsistent(t *testing.T) {
	dir := t.TempDir()
	store, _, ids, _, q := zz2FailedJob(t, dir, 16)
	zz2CloseFixture(t, store, q)
	b, err := os.ReadFile(filepath.Join(dir, reasonsFileName))
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]jobReasons
	if err = json.Unmarshal(b, &saved); err != nil {
		t.Fatalf("sidecar corrupt: %v", err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, id := range ids {
		if e := reopened.data.Jobs[id].Value.Error; e == nil || e.Reason != "provider returned HTTP 422" {
			t.Errorf("%s: %+v", id, e)
		}
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, reasonsFileName+".*"))
	if len(leftovers) != 0 {
		t.Errorf("temp files left: %v", leftovers)
	}
}

// A damaged advisory file costs the hint and nothing else.
func TestRegressionDamagedSidecarOnlyLosesTheHint(t *testing.T) {
	dir := t.TempDir()
	store, _, ids, _, q := zz2FailedJob(t, dir, 1)
	zz2CloseFixture(t, store, q)
	path := filepath.Join(dir, reasonsFileName)
	for name, content := range map[string]string{
		"truncated":   `{"` + ids[0] + `":{"job":"x`,
		"wrong type":  `{"` + ids[0] + `":{"items":{"a":1}}}`,
		"null":        `null`,
		"array":       `[]`,
		"binary":      "\x00\xff\xfe",
		"empty":       ``,
		"unknown job": `{"gone":{"job":"x"}}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		reopened, err := OpenStore(dir)
		if err != nil {
			t.Fatalf("%s: store refused to open: %v", name, err)
		}
		e := reopened.data.Jobs[ids[0]].Value.Error
		if e == nil || e.Code == "" || e.Reason != "" {
			t.Errorf("%s: %+v", name, e)
		}
		reopened.Close()
	}
}

// The sidecar sits outside the checksummed log, so restored text goes through SafeReason again.
func TestRegressionTamperedSidecarIsSanitizedOnRestore(t *testing.T) {
	dir := t.TempDir()
	store, _, ids, _, q := zz2FailedJob(t, dir, 1)
	zz2CloseFixture(t, store, q)
	raw, err := json.Marshal(map[string]jobReasons{ids[0]: {Job: "see https://cdn.example/a?sig=PRIVATE and " + strings.Repeat("x", 500)}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, reasonsFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got := reopened.data.Jobs[ids[0]].Value.Error.Reason
	if strings.Contains(got, "PRIVATE") || len([]rune(got)) > maxReasonRunes {
		t.Fatalf("restored %q", got)
	}
}

// No size guard exists; this records the startup cost of a large file rather than asserting one.
func TestRegressionLargeSidecarStillOpens(t *testing.T) {
	dir := t.TempDir()
	store, _, _, _, q := zz2FailedJob(t, dir, 1)
	zz2CloseFixture(t, store, q)
	var b bytes.Buffer
	b.WriteString(`{`)
	for i := 0; b.Len() < 32<<20; i++ {
		fmt.Fprintf(&b, `"gone-%d":{"job":"%s"},`, i, strings.Repeat("x", 160))
	}
	b.WriteString(`"end":{}}`)
	if err := os.WriteFile(filepath.Join(dir, reasonsFileName), b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	t.Logf("open with %d MB sidecar took %v", b.Len()>>20, time.Since(start))
}
