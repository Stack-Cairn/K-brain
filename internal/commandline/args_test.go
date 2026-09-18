package commandline

import (
	"reflect"
	"testing"
)

func TestSplit(t *testing.T) {
	for _, tc := range []struct {
		text string
		want []string
		fail bool
	}{
		{`"C:\Program Files\Editor\edit.exe" --wait`, []string{`C:\Program Files\Editor\edit.exe`, "--wait"}, false},
		{`code --wait 'two words'`, []string{"code", "--wait", "two words"}, false},
		{`editor "$HOME" ; echo`, []string{"editor", "$HOME", ";", "echo"}, false},
		{`editor ""`, []string{"editor", ""}, false},
		{`editor "bad`, nil, true},
	} {
		got, err := Split(tc.text)
		if (err != nil) != tc.fail || (!tc.fail && !reflect.DeepEqual(got, tc.want)) {
			t.Fatalf("%q: %v %v", tc.text, got, err)
		}
	}
}
