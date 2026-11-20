package config

import (
	"strings"
	"testing"
)

func TestParseConfigHookMatcher(t *testing.T) {
	const base = `{"providers":{"a":{"api":"openai-completions","baseUrl":"https://a.invalid/v1","models":[{"id":"m"}]}},"hooks":%s}`
	parse := func(hooks string) (Config, error) {
		var cfg Config
		err := parseConfigJSONC([]byte(strings.Replace(base, "%s", hooks, 1)), &cfg)
		return cfg, err
	}
	cfg, err := parse(`{"PreToolUse":[{"command":"guard.sh","matcher":"bash|edit"}],"Stop":[{"command":"notify"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Hooks["PreToolUse"][0].Matcher; got != "bash|edit" {
		t.Fatalf("matcher = %q", got)
	}
	for _, bad := range []string{
		`{"PostToolUse":[{"command":"x","matcher":"[bash"}]}`,
		`{"UserPromptSubmit":[{"command":"x","matcher":"bash"}]}`,
		`{"Stop":[{"command":"x","matcher":"*"}]}`,
	} {
		if _, err := parse(bad); err == nil || !strings.Contains(err.Error(), "matcher") {
			t.Fatalf("%s accepted: %v", bad, err)
		}
	}
	// Existing configs without a matcher keep loading.
	if _, err := parse(`{"PostToolUseFailure":[{"command":"x","shell":"sh","timeout":5}]}`); err != nil {
		t.Fatal(err)
	}
}

func TestCompileHookMatcherAnchors(t *testing.T) {
	re, err := CompileHookMatcher("bash")
	if err != nil || re.MatchString("bashrc") || !re.MatchString("bash") {
		t.Fatalf("re=%v err=%v", re, err)
	}
	for _, all := range []string{"", " ", "*"} {
		if re, err := CompileHookMatcher(all); re != nil || err != nil {
			t.Fatalf("%q: re=%v err=%v", all, re, err)
		}
	}
}
