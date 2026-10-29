package privacy

import (
	"strings"
	"testing"
)

// Fourth review round for 205d462 (maskTokenRx split in locateIPv6). These are
// guards: they pass on 205d462 and pin the properties the split relies on.

// Every label MaskText can emit before IPV6 runs (underscores: API_KEY,
// ACCESS_KEY, PRIVATE_KEY; digits: IPV6 itself via remembered tokens) is
// matched by maskTokenRx exactly, never across two adjacent tokens.
func TestReview4MaskTokenRxMatchesGeneratedTokens(t *testing.T) {
	for _, r := range rules {
		for _, seq := range []int{1, 9999, 10000, 123456} {
			token := "__KBR_" + r.name + "_" + formatSeq(seq) + "__"
			pair := token + token
			got := maskTokenRx.FindAllStringIndex(pair, -1)
			if len(got) != 2 || got[0][0] != 0 || got[0][1] != len(token) || got[1][1] != len(pair) {
				t.Errorf("maskTokenRx on %q = %v; want two exact tokens", pair, got)
			}
		}
	}
}

// Offsets after the split: tokens with underscore labels and 5-digit sequence
// numbers before, between and after the address; only the address is replaced
// and the tokens around it stay byte-identical.
func TestReview4SplitOffsetsKeepNeighbourTokensIntact(t *testing.T) {
	for _, tc := range []struct{ match, want string }{
		{"__KBR_API_KEY_12345__:2001:db8::1", "2001:db8::1"},
		{"__KBR_ACCESS_KEY_0001____KBR_IP_0002__:fe80::1c2d:__KBR_IP_0003__", "fe80::1c2d"},
		{"2001:db8:64::__KBR_IP_0004__", "2001:db8:64::"},
		{"src:2001:db8::5efe:__KBR_IP_10000__", "c:2001:db8::5efe"},
		{"__KBR_IPV6_0005__:__KBR_IP_0006__", ""},
		{"crate::abc1__KBR_EMAIL_0007__::fs", ""},
	} {
		start, end, ok := locateIPv6(tc.match)
		if tc.want == "" {
			if ok {
				t.Errorf("locateIPv6(%q) = %q; want none", tc.match, tc.match[start:end])
			}
			continue
		}
		if !ok || tc.match[start:end] != tc.want {
			t.Errorf("locateIPv6(%q) = %d,%d,%v; want %q", tc.match, start, end, ok, tc.want)
		}
	}
}

// End to end through MaskText with an underscore-label token in front of an
// embedded-IPv4 address: nothing leaks and restoring the values gives back the
// original text.
func TestReview4MaskTextRestoresAroundUnderscoreLabelToken(t *testing.T) {
	input := "key=sk-abcdefghijklmnopqrstuvwx:2001:db8:64::192.0.2.33 ok"
	masked, values := MaskText(input)
	for _, leak := range []string{"sk-abcdefghijklmnopqrstuvwx", "2001:db8:64::", "192.0.2.33"} {
		if strings.Contains(masked, leak) {
			t.Errorf("MaskText(%q) = %q; %q leaked", input, masked, leak)
		}
	}
	restored := masked
	for token, value := range values {
		restored = strings.ReplaceAll(restored, token, value)
	}
	if restored != input {
		t.Errorf("restore(%q) = %q; want %q", masked, restored, input)
	}
}
