package privacy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMaskTextAndRestore(t *testing.T) {
	masked, values := MaskText(`contact alice@example.com, 中文名@例子.中国, phone 13812345678, token=sk_live_1234567890, key ghp_123456789012345678901234`)
	if strings.Contains(masked, "alice@example.com") || strings.Contains(masked, "13812345678") || strings.Contains(masked, "sk_live_1234567890") || strings.Contains(masked, "ghp_123456789012345678901234") {
		t.Fatalf("sensitive values remained: %q", masked)
	}
	if len(values) != 5 {
		t.Fatalf("values = %d, want 5", len(values))
	}
	for token, value := range values {
		if !strings.Contains(masked, token) {
			t.Fatalf("masked text missing token %q", token)
		}
		if value == "" {
			t.Fatalf("empty value for %q", token)
		}
	}
}

func TestMaskTextValidatesStructuredSecrets(t *testing.T) {
	input := strings.Join([]string{
		"aws=AKIAIOSFODNN7EXAMPLE",
		"db=postgres://alice:s3cretPass@db.internal:5432/app",
		"card=4111111111111111",
		"not-card=4111111111111112",
		"ip=2001:db8::42",
	}, " ")
	masked, values := MaskText(input)
	for _, plain := range []string{"AKIAIOSFODNN7EXAMPLE", "s3cretPass", "4111111111111111", "2001:db8::42"} {
		if strings.Contains(masked, plain) {
			t.Fatalf("sensitive value %q remained in %q", plain, masked)
		}
	}
	if !strings.Contains(masked, "4111111111111112") {
		t.Fatalf("invalid card was masked: %q", masked)
	}
	if len(values) < 4 {
		t.Fatalf("values = %d, want at least 4", len(values))
	}
}

func TestMaskTextReusesTokenWithinTTL(t *testing.T) {
	first, firstValues := MaskText("email alice@example.com")
	second, secondValues := MaskText("repeat alice@example.com")
	if len(firstValues) != 1 || len(secondValues) != 1 {
		t.Fatalf("unexpected mappings: first=%v second=%v", firstValues, secondValues)
	}
	var firstToken string
	for token := range firstValues {
		firstToken = token
	}
	if !strings.Contains(second, firstToken) {
		t.Fatalf("token was not reused: first=%q second=%q", first, second)
	}
}

func TestMaskTextMaskitRulesAndValidation(t *testing.T) {
	input := `anthropic=sk-ant-api03-abcdefghijklmnopqrstuvwxyz1234567890 aws=AKIAIOSFODNN7EXAMPLE db=postgres://u:secret123@db.internal card=4111111111111111 iban=GB82WEST12345698765432 mac=AA:BB:CC:DD:EE:FF 010-87654321`
	masked, values := MaskText(input)
	for _, plain := range []string{"sk-ant-api03-abcdefghijklmnopqrstuvwxyz1234567890", "AKIAIOSFODNN7EXAMPLE", "secret123", "4111111111111111", "GB82WEST12345698765432", "AA:BB:CC:DD:EE:FF", "010-87654321"} {
		if strings.Contains(masked, plain) {
			t.Fatalf("sensitive value %q remained: %q", plain, masked)
		}
	}
	if len(values) < 7 {
		t.Fatalf("values = %d, want at least 7", len(values))
	}
	if !strings.Contains(masked, "postgres://u:") || !strings.Contains(masked, "@db.internal") {
		t.Fatalf("connection string structure was not preserved: %q", masked)
	}
}

func TestMaskTextRejectsMalformedStructuredValues(t *testing.T) {
	input := "fake-ip 999.999.999.999 fake-jwt eyJnot-json.bad-payload.signature fake-card 4111111111111112"
	masked, values := MaskText(input)
	if len(values) != 0 {
		t.Fatalf("malformed values were masked: %v", values)
	}
	if masked != input {
		t.Fatalf("malformed values changed: %q", masked)
	}
}

func TestTransportMasksRequestAndRestoresStreamingResponse(t *testing.T) {
	SetEnabled(true)
	defer SetEnabled(false)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "alice@example.com") {
			t.Errorf("request was not masked: %s", body)
		}
		token := ""
		start := strings.Index(string(body), "__KBR_EMAIL_")
		if start >= 0 {
			rest := string(body)[start:]
			if end := strings.Index(rest[2:], "__"); end >= 0 {
				token = rest[:end+4]
			}
		}
		if token == "" {
			t.Errorf("masked token missing from request: %s", body)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, part := range []string{"data: " + token[:len(token)/2], token[len(token)/2:] + "\n\n"} {
			_, _ = w.Write([]byte(part))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer srv.Close()
	client := &http.Client{Transport: WrapTransport(http.DefaultTransport)}
	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"message":"alice@example.com"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "data: alice@example.com\n\n" {
		t.Fatalf("restored response = %q", out)
	}
}

func TestTransportRestoresRememberedTokenWithoutNewRequestMatch(t *testing.T) {
	SetEnabled(true)
	defer SetEnabled(false)
	_, values := MaskText(`{"message":"alice@example.com"}`)
	var token string
	for token = range values {
		break
	}
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"echo":"` + token + `"}`)), Request: r}, nil
	})
	req, _ := http.NewRequest(http.MethodPost, "https://example.test", strings.NewReader(`{"message":"no secret"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&Transport{Base: base}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "alice@example.com") {
		t.Fatalf("remembered token was not restored: %s", out)
	}
}

func TestTransportSkipsBinaryBodies(t *testing.T) {
	SetEnabled(true)
	defer SetEnabled(false)
	var got []byte
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got, _ = io.ReadAll(r.Body)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("binary")), Request: r}, nil
	})
	payload := []byte{0, 1, 2, 3, 0xff, 0x00}
	req, _ := http.NewRequest(http.MethodPost, "https://example.test", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := (&Transport{Base: base}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if string(got) != string(payload) {
		t.Fatalf("binary body changed: %v", got)
	}
}

func TestTransportLeavesOversizedBodyComplete(t *testing.T) {
	SetEnabled(true)
	defer SetEnabled(false)
	want := strings.Repeat("x", maxInspectableBody+1024)
	var got string
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
	})
	req, _ := http.NewRequest(http.MethodPost, "https://example.test", strings.NewReader(want))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&Transport{Base: base}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got != want {
		t.Fatalf("oversized body was truncated: got %d, want %d", len(got), len(want))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
