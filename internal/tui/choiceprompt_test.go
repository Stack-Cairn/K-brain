package tui

import "testing"

func TestResolveChoice(t *testing.T) {
	opts := []string{"alpha", "beta", "+ Create new project"}
	for _, tc := range []struct {
		value string
		want  string
	}{
		{"", "alpha"},
		{"1", "alpha"},
		{"2", "beta"},
		{"3", "+ Create new project"},
		{"beta", "beta"},
		{"9", ""},
		{"xyz", ""},
	} {
		if got := resolveChoice(tc.value, opts); got != tc.want {
			t.Errorf("resolveChoice(%q) = %q, want %q", tc.value, got, tc.want)
		}
	}
}
