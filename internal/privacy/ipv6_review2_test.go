package privacy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Second review round for c3f73e6 (locateIPv6).
//
// The cut point is "after the last non-hex letter", so a tag whose tail is hex
// ("src" -> "c", "ip6" -> "6", "interface" -> "face") is glued onto the address.
// For a compressed address that still parses ("c:fe80::1"), but for the full
// 8-group form (and 7 groups + "::") the extra group makes it 9 groups, the
// parse fails and locate gives up without trying the address after the tag
// colon. Same when the tag is entirely hex and longer than 4 ("added").
var hexTailTagInputs = []struct{ input, leak string }{
	{"nft trace: src:2001:db8:85a3:0:0:8a2e:370:7334 dport 22", "2001:db8:85a3:0:0:8a2e:370:7334"},
	{"v=spf1 ip6:2001:db8:85a3:8d3:1319:8a2e:370:7348 -all", "2001:db8:85a3:8d3:1319:8a2e:370:7348"},
	{"interface:2001:0db8:85a3:0000:0000:8a2e:0370:7334", "2001:0db8:85a3:0000:0000:8a2e:0370:7334"},
	{"via:2001:db8:1:2:3:4:5::", "2001:db8:1:2:3:4:5::"},
	{"peer added:2001:db8::1", "2001:db8::1"},
}

func TestReview2IPv6HexTailTagFullAddressIsRedacted(t *testing.T) {
	for _, tc := range hexTailTagInputs {
		if masked, changed := RedactText(tc.input); !changed || strings.Contains(masked, tc.leak) {
			t.Errorf("RedactText(%q) = %q, %v; IPv6 %q leaked", tc.input, masked, changed, tc.leak)
		}
	}
}

func TestReview2IPv6HexTailTagFullAddressMaskedOnTheWire(t *testing.T) {
	SetEnabled(true)
	defer SetEnabled(false)
	for _, tc := range hexTailTagInputs {
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

// The tag-colon branch only runs when a letter/underscore precedes the colon
// inside the run (start > 0). When the tag ends in a character outside the run
// ("-", ".", CJK, "]"), the run itself begins with a single ':' and start stays
// 0, so ":2001:db8::1" is parsed as-is and fails. Hyphenated header-style tags
// and Chinese labels with an ASCII colon are common in logs.
func TestReview2IPv6AfterNonWordTagColonIsRedacted(t *testing.T) {
	for _, tc := range []struct{ input, leak string }{
		{"remote-addr:2001:db8::1 status=200", "2001:db8::1"},
		{"X-Forwarded-For:2001:db8::7", "2001:db8::7"},
		{"客户端地址:2001:db8::1", "2001:db8::1"},
		{"ip.src:fe80::1c2d:3e4f", "fe80::1c2d:3e4f"},
		{"relay alice@example.com:2001:db8::25 accepted", "2001:db8::25"},
	} {
		if masked, changed := RedactText(tc.input); !changed || strings.Contains(masked, tc.leak) {
			t.Errorf("RedactText(%q) = %q, %v; IPv6 %q leaked", tc.input, masked, changed, tc.leak)
		}
		if masked, _ := MaskText(tc.input); strings.Contains(masked, tc.leak) {
			t.Errorf("MaskText(%q) = %q; IPv6 %q leaked", tc.input, masked, tc.leak)
		}
	}
}

// Guard for any fix of the above: code paths, timestamps and line:col markers
// stay untouched; compressed tagged forms keep working (passes today).
func TestReview2IPv6GuardsStillHold(t *testing.T) {
	for _, input := range []string{
		"use std::io::Result;",
		"crate::abc1 and Foo::f00d2()",
		"  --> src/main.rs:12:34:",
		"2026-10-29T12:34:56Z",
		"[29/Oct/2026:12:34:56 +0000]",
		"impl ad::Be for cafe::Dead {}",
	} {
		if masked, changed := RedactText(input); changed || masked != input {
			t.Errorf("RedactText(%q) = %q, %v; want unchanged", input, masked, changed)
		}
	}
	for _, tc := range []struct{ input, leak string }{
		{"src:fe80::1c2d:3e4f", "fe80::1c2d:3e4f"},
		{"ip6:2001:db8:4860::/32", "2001:db8:4860::"},
		{"lo:::1", "::1"},
	} {
		if masked, _ := RedactText(tc.input); strings.Contains(masked, tc.leak) {
			t.Errorf("RedactText(%q) = %q; %q leaked", tc.input, masked, tc.leak)
		}
	}
}
