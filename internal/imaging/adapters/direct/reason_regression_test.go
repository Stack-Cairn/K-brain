package direct

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func zzProviderReason(t *testing.T, status int, message string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"error": map[string]any{"code": "x", "message": message}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		w.Write(body)
	}))
	defer srv.Close()
	a := adapter(t, OpenAIID, options(srv.Client(), srv.URL))
	_, err = a.Submit(context.Background(), request(OpenAIID))
	var ae *imaging.AdapterError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v", err)
	}
	return imaging.PublicError(&imaging.ImageError{Code: ae.Code, Reason: ae.Reason}).Reason
}

// Fails on the reviewed tree: error.message goes real HTTP -> adapter -> PublicError and these
// secrets reach the wire. Comment on httpFailure's caller still says bodies are never exposed.
func TestReasonRegressionProviderMessageCannotLeakThroughReason(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		secrets       []string
	}{
		{"signed_url_without_scheme", "fetch failed for cdn.example.com/a.png?X-Amz-Credential=AKIAIOSFODNN7EXAMPLE&X-Amz-Signature=deadbeef01", []string{"cdn.example.com", "AKIAIOSFODNN7EXAMPLE", "deadbeef01"}},
		{"percent_encoded_url", "callback=https%3A%2F%2Fcdn.example.com%2Fa.png%3Fsig%3Dabc123def rejected", []string{"cdn.example.com", "abc123def"}},
		{"json_escaped_url", `{"url":"https:\u002f\u002fcdn.example.com\u002fa.png?sig=abc123def"}`, []string{"cdn.example.com", "abc123def"}},
		{"api_key_assignment", "invalid api_key=abcd1234efgh5678", []string{"abcd1234efgh5678"}},
		{"api_key_header", "rejected x-api-key: abcd1234efgh5678", []string{"abcd1234efgh5678"}},
		{"token_scheme", "Authorization: Token abcd1234efgh5678", []string{"abcd1234efgh5678"}},
		{"password_assignment", "login failed password=hunter2", []string{"hunter2"}},
		{"uppercase_key", "invalid key SK-ABCDEFGHIJKL", []string{"ABCDEFGHIJKL"}},
		{"aws_key_id", "account AKIAIOSFODNN7EXAMPLE is suspended", []string{"AKIAIOSFODNN7EXAMPLE"}},
		{"short_jwt", "token eyJhbGciOiJub25lIn0.eyJzdWIiOiIxIn0.c2lnbmF0dXJl expired", []string{"eyJhbGci"}},
		{"internal_host_lookup", "dial tcp: lookup gpu-01.corp.internal: no such host", []string{"gpu-01.corp.internal"}},
		{"internal_host_port", "upstream gpu-01.corp.internal:8443 is down", []string{"gpu-01.corp.internal"}},
		{"bare_ipv6", "connect fe80::1ff:fe23:4567:890a failed", []string{"fe80::1ff"}},
		{"windows_path_with_space", `open C:\Program Files\KBrain\secret.key failed`, []string{"KBrain", "secret.key"}},
		{"windows_user_with_space", `open C:\Users\John Doe\AppData\cfg.json failed`, []string{"AppData", "cfg.json"}},
		{"windows_root_file", `open C:\secret.key failed`, []string{"secret.key"}},
		{"unc_path", `open \\fileserver\share\secret.key failed`, []string{"fileserver", "secret.key"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := zzProviderReason(t, 400, tc.message)
			for _, s := range tc.secrets {
				if strings.Contains(got, s) {
					t.Errorf("reason %q leaks %q", got, s)
				}
			}
		})
	}
}

// Fails on the reviewed tree: the same body TestContractErrorsAndNoRetry uses for its "bad input"
// row. That test still passes only because assertError does not look at Reason.
func TestReasonRegressionContractSentinelStaysOutOfReason(t *testing.T) {
	got := zzProviderReason(t, 400, "SECRET_SENTINEL")
	if strings.Contains(got, "SECRET_SENTINEL") {
		t.Fatalf("reason %q exposes the error body", got)
	}
}

// Passes: the input this path feeds the scheduler. The provider answered 401 and the submit
// timer then interrupted the body read, which is the reachable form of the timer rewrite.
func TestReasonVerifiedInterruptedErrorBodyStillReportsTheProviderStatus(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}))
	defer srv.Close()
	defer close(done)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	a := adapter(t, OpenAIID, options(srv.Client(), srv.URL))
	_, err := a.Submit(ctx, request(OpenAIID))
	var ae *imaging.AdapterError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v", err)
	}
	if ae.Code != imaging.CodeAuthRequired || ae.Delivery != imaging.DeliveryRejected || ae.Reason != "provider returned HTTP 401" {
		t.Fatalf("%+v", ae)
	}
	if ctx.Err() == nil {
		t.Fatal("expected the call context to have ended")
	}
}
