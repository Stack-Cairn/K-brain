package privacy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Review regressions for bc73e7a. Inputs are real tool-output shapes: SPF TXT
// records (dig), SMTP address literals (Received headers / MTA logs), and
// tag-prefixed log fields. Before bc73e7a the hex-only class happened to start
// the match at the hex digit right before the tag colon ("6:2001:db8::/" for
// "ip6:"), which parses, so these were masked. The new rule takes the whole
// identifier run ("ip6:2001:db8::") which fails net.ParseIP and is released.
var taggedIPv6Inputs = []struct{ input, leak string }{
	{"v=spf1 ip6:2001:db8:4860::/32 include:_spf.example.com ~all", "2001:db8:4860::"},
	{"Received: from mx.example.com ([IPv6:2001:db8::25]) by relay", "2001:db8::25"},
	{"nft trace: src:fe80::1c2d:3e4f dport 22", "fe80::1c2d:3e4f"},
	{"route via:2001:db8::1 dev eth0", "2001:db8::1"},
	{"listen tcp6:2001:db8::1", "2001:db8::1"},
}

func TestReviewIPv6TaggedAddressIsRedacted(t *testing.T) {
	for _, tc := range taggedIPv6Inputs {
		if masked, changed := RedactText(tc.input); !changed || strings.Contains(masked, tc.leak) {
			t.Errorf("RedactText(%q) = %q, %v; IPv6 %q leaked", tc.input, masked, changed, tc.leak)
		}
	}
}

// Same inputs through the real request path: WrapTransport -> MaskText -> HTTP
// body seen by the model provider.
func TestReviewIPv6TaggedAddressMaskedOnTheWire(t *testing.T) {
	SetEnabled(true)
	defer SetEnabled(false)
	for _, tc := range taggedIPv6Inputs {
		var seen string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			seen = string(body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}))
		client := &http.Client{Transport: WrapTransport(http.DefaultTransport)}
		req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"message":"`+tc.input+`"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			srv.Close()
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		srv.Close()
		if strings.Contains(seen, tc.leak) {
			t.Errorf("provider received IPv6 %q unmasked: %s", tc.leak, seen)
		}
	}
}

// iputils ping prints "64 bytes from <addr>: icmp_seq=..."; Go/libc errors use
// "<addr>: connection refused". The trailing colon is pulled into the run and
// the whole run fails to parse. (Also leaked before bc73e7a; reported because
// this commit owns the IPv6 boundary.)
func TestReviewIPv6FollowedByColonIsRedacted(t *testing.T) {
	for _, tc := range []struct{ input, leak string }{
		{"64 bytes from 2001:db8::1: icmp_seq=1 ttl=64 time=0.045 ms", "2001:db8::1"},
		{"connect to 2001:db8:85a3::8a2e:370:7334: connection refused", "2001:db8:85a3::8a2e:370:7334"},
	} {
		if masked, changed := RedactText(tc.input); !changed || strings.Contains(masked, tc.leak) {
			t.Errorf("RedactText(%q) = %q, %v; IPv6 %q leaked", tc.input, masked, changed, tc.leak)
		}
	}
}

// Guard for whatever fix is chosen: Rust/C++ paths and timestamps must stay
// untouched (passes today).
func TestReviewIPv6TagFixKeepsCodePathsUntouched(t *testing.T) {
	for _, input := range []string{
		"use std::io::Result;",
		"crate::fs::read_to_string",
		"  --> src/main.rs:4:5",
		"[29/Oct/2026:12:34:56 +0000]",
		"impl ad::Be for cafe::Dead {}",
		"error: tag:value:other",
	} {
		if masked, changed := RedactText(input); changed || masked != input {
			t.Errorf("RedactText(%q) = %q, %v; want unchanged", input, masked, changed)
		}
	}
}

func BenchmarkReviewIPv6MaskText32MB(b *testing.B) {
	for _, tc := range []struct{ name, unit string }{
		{"identifier", "abcdefghij_"},
		{"rustpaths", "std::io::Result "},
		{"colonrun", "a1:"},
	} {
		body := strings.Repeat(tc.unit, (32<<20)/len(tc.unit))
		b.Run(tc.name, func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			for i := 0; i < b.N; i++ {
				MaskText(body)
			}
		})
	}
}
