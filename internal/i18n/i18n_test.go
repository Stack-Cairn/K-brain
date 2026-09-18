package i18n

import "testing"

func TestLanguageFallback(t *testing.T) {
	for _, language := range []string{"", "en", "fr", "zh_CN"} {
		if Normalize(language) != English || Text(language, "Commands") != "Commands" {
			t.Fatal(language)
		}
	}
	if !Valid(Chinese) || !Valid(English) || Valid("fr") || Valid("") {
		t.Fatal("language validation")
	}
	if Text(Chinese, "Commands") != "命令" {
		t.Fatal("translation missing")
	}
	if Text(Chinese, "user supplied model name") != "user supplied model name" {
		t.Fatal("unknown text modified")
	}
}
