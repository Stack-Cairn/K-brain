package imaging

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSafeReasonRedactsSensitiveText(t *testing.T) {
	cases := map[string]string{
		"url":    "failed calling https://api.moonup.example/v1/images?key=abc123 now",
		"bearer": "bad header Bearer sk-live-1234567890abcdef",
		"key":    "invalid key sk-proj-AbCdEf123456",
		"long":   "token 0123456789abcdef0123456789abcdef0123 rejected",
		"ip":     "dial 10.1.2.3:8080 refused",
		"path":   `open C:\Users\me\secret\cfg.json failed`,
		"unix":   "open /home/me/.config/app/cfg.json failed",
		"email":  "account me@example.com blocked",
	}
	secrets := map[string][]string{
		"url":    {"moonup", "abc123"},
		"bearer": {"sk-live", "1234567890"},
		"key":    {"AbCdEf123456"},
		"long":   {"0123456789abcdef"},
		"ip":     {"10.1.2.3"},
		"path":   {"secret", "Users"},
		"unix":   {"/home/me", "cfg.json"},
		"email":  {"me@example.com"},
	}
	for name, in := range cases {
		got := SafeReason(in)
		for _, s := range secrets[name] {
			if strings.Contains(got, s) {
				t.Errorf("%s: %q still contains %q", name, got, s)
			}
		}
		if !strings.Contains(got, "[redacted]") {
			t.Errorf("%s: %q has no redaction marker", name, got)
		}
	}
}

func TestSafeReasonKeepsPlainProviderText(t *testing.T) {
	in := "provider returned HTTP 422: size 9:16 at 4K is not supported"
	if got := SafeReason(in); got != in {
		t.Fatalf("plain text changed: %q", got)
	}
}

func TestSafeReasonIsOneBoundedLine(t *testing.T) {
	got := SafeReason("line one\nline two\t\x00tail " + strings.Repeat("字", 400))
	if strings.ContainsAny(got, "\n\t\x00") {
		t.Fatalf("control characters survived: %q", got)
	}
	if n := utf8.RuneCountInString(got); n > maxReasonRunes {
		t.Fatalf("reason has %d runes, limit %d", n, maxReasonRunes)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("reason is not valid UTF-8")
	}
	if SafeReason("") != "" {
		t.Fatal("empty reason must stay empty")
	}
}

func TestTransportReasonUsesFixedPhrases(t *testing.T) {
	wrapped := &net.OpError{Op: "dial", Err: errors.New("dial tcp 10.0.0.1:443: secret-host refused")}
	for err, want := range map[error]string{
		context.DeadlineExceeded:                   "request timed out",
		context.Canceled:                           "request was cancelled",
		&net.DNSError{Name: "secret-host.example"}: "provider host could not be resolved",
		wrapped: "connection to the provider failed",
		errors.New("anything https://x.example/?key=abc"): "",
	} {
		got := TransportReason(err)
		if got != want {
			t.Errorf("TransportReason(%v) = %q, want %q", err, got, want)
		}
		if strings.Contains(got, "secret") || strings.Contains(got, "abc") {
			t.Errorf("reason leaked error text: %q", got)
		}
	}
}

func TestPublicErrorCarriesSanitizedReason(t *testing.T) {
	e := PublicError(&ImageError{Code: CodeResultUnknown, Reason: "no response at https://u.example/p?k=1"})
	if e.Code != CodeResultUnknown {
		t.Fatalf("code rewritten to %q", e.Code)
	}
	if strings.Contains(e.Reason, "u.example") || e.Reason == "" {
		t.Fatalf("reason = %q", e.Reason)
	}
	if e.Message != "Image execution result is unknown" {
		t.Fatalf("message = %q", e.Message)
	}
}

func TestImageErrorWireReasonIsOptional(t *testing.T) {
	raw, err := json.Marshal(ImageError{Code: CodeUnavailable})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "reason") {
		t.Fatalf("empty reason must be omitted: %s", raw)
	}
	raw, err = json.Marshal(ImageError{Code: CodeNoImageOutput, Reason: "provider response contained no image"})
	if err != nil {
		t.Fatal(err)
	}
	var back ImageError
	if err = json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Code != CodeNoImageOutput || back.Reason != "provider response contained no image" {
		t.Fatalf("round trip lost data: %s", raw)
	}
}

func TestSafeEngineErrorKeepsAdapterReason(t *testing.T) {
	got := safeEngineError(&AdapterError{Code: CodeResultUnknown, Delivery: DeliveryUncertain, Reason: "no response from the provider: request timed out"})
	if got.Code != CodeResultUnknown || got.Reason != "no response from the provider: request timed out" {
		t.Fatalf("got %+v", got)
	}
	got = safeEngineError(context.DeadlineExceeded)
	if got.Code != CodeUnavailable || got.Reason != "request timed out" {
		t.Fatalf("got %+v", got)
	}
	got = safeEngineError(errors.New("boom https://x.example"))
	if got.Code != CodeUnavailable || got.Reason != "" {
		t.Fatalf("untyped error must not leak text: %+v", got)
	}
}
