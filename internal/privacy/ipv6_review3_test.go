package privacy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Third review round for a8b9ee7.
//
// MaskText runs the IP rule before IPV6, so an IPv4-embedded address
// ("2001:db8:64::192.0.2.33", NAT64 / ISATAP style) becomes
// "2001:db8:64::__KBR_IP_0001__". The IPV6 run now includes the token (the
// class has '_'), locate cuts after the token's last '_' and finds nothing, so
// the IPv6 prefix is sent in clear. RedactText's "[REDACTED_IP]" ends the run
// at '[', so "2001:db8:64::" is located and redacted: the two paths disagree.
func TestReview3IPv6PrefixBeforeEmbeddedIPv4MaskedLikeRedact(t *testing.T) {
	for _, tc := range []struct{ input, leak string }{
		{"nat64 2001:db8:64::192.0.2.33 reachable", "2001:db8:64::"},
		{"isatap 2001:db8:1::5efe:10.0.0.7", "2001:db8:1::5efe"},
	} {
		redacted, changed := RedactText(tc.input)
		if !changed || strings.Contains(redacted, tc.leak) {
			t.Errorf("RedactText(%q) = %q, %v; %q leaked", tc.input, redacted, changed, tc.leak)
		}
		masked, values := MaskText(tc.input)
		if strings.Contains(masked, tc.leak) {
			t.Errorf("MaskText(%q) = %q (values %v); RedactText hides %q but MaskText sends it", tc.input, masked, values, tc.leak)
		}
	}
}

// Guard for the claim "a located span never overlaps a token": a token right
// before the address is kept intact, the address gets its own token, and the
// real restore path turns the adjacent pair back into the original text
// (passes today).
func TestReview3AdjacentTokensRoundTripThroughTransport(t *testing.T) {
	SetEnabled(true)
	defer SetEnabled(false)
	input := "relay alice@example.com:2001:db8::25 地址:fe80::1c2d [REDACTED_EMAIL]:2001:db8::26"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		for _, leak := range []string{"alice@example.com", "2001:db8::25", "fe80::1c2d", "2001:db8::26"} {
			if strings.Contains(string(body), leak) {
				t.Errorf("provider received %q: %s", leak, body)
			}
		}
		if strings.Count(string(body), "__KBR_") != 4 {
			t.Errorf("want 4 intact tokens, got body %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// Echo the masked body back split mid-token to exercise restoreReader.
		mid := len(body) / 2
		for _, part := range [][]byte{body[:mid], body[mid:]} {
			_, _ = w.Write(part)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer srv.Close()
	client := &http.Client{Transport: WrapTransport(http.DefaultTransport)}
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(input))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != input {
		t.Fatalf("restored = %q, want %q", out, input)
	}
}

// Guard: retry-after-tag-colon and leading-colon stripping must not turn code,
// timestamps, or fingerprints into IPv6 (passes today).
func TestReview3RetryDoesNotWidenFalsePositives(t *testing.T) {
	for _, input := range []string{
		"use std::io::Result;",
		"crate::abc1 and Foo::f00d2()",
		"  --> src/main.rs:12:34:",
		"2026-10-29T12:34:56Z [29/Oct/2026:12:34:56 +0000]",
		"SHA256:nThbg6kXUpJWGl7E1IGOCspRomTxdCARLviKw6E5SY8",
		"image: nginx:1.25 tag:v1:2",
		"ok: 3 passed: 0 failed:",
	} {
		if masked, changed := RedactText(input); changed || masked != input {
			t.Errorf("RedactText(%q) = %q, %v; want unchanged", input, masked, changed)
		}
		if masked, values := MaskText(input); len(values) != 0 || masked != input {
			t.Errorf("MaskText(%q) = %q, %v; want unchanged", input, masked, values)
		}
	}
}
