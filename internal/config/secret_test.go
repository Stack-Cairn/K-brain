package config

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestResolveSecretEnvVar(t *testing.T) {
	t.Setenv("K_BRAIN_SECRET_TEST", "s3cr3t")
	for _, ref := range []string{"$K_BRAIN_SECRET_TEST", "${K_BRAIN_SECRET_TEST}"} {
		got, err := ResolveSecret(ref)
		if err != nil || got != "s3cr3t" {
			t.Fatalf("%s: got %q err %v", ref, got, err)
		}
	}
}

func TestResolveSecretUnsetVar(t *testing.T) {
	for _, ref := range []string{"$K_BRAIN_SECRET_UNSET", "${K_BRAIN_SECRET_UNSET}"} {
		got, err := ResolveSecret(ref)
		if err == nil || !strings.Contains(err.Error(), "K_BRAIN_SECRET_UNSET") {
			t.Fatalf("%s: expected unset-var error naming the var, got %q err %v", ref, got, err)
		}
		if got != "" {
			t.Fatalf("%s: expected empty on error, got %q", ref, got)
		}
	}
}

func TestResolveSecretCommand(t *testing.T) {
	command := "!printf secret-value"
	if runtime.GOOS == "windows" {
		command = "!cmd.exe /c echo secret-value"
	}
	got, err := ResolveSecret(command)
	if err != nil || got != "secret-value" {
		t.Fatalf("!printf: got %q err %v", got, err)
	}

	if _, err := ResolveSecret("!exit 1"); err == nil {
		t.Fatal("!exit 1: expected error")
	}
}

func TestResolveSecretLiteral(t *testing.T) {
	got, err := ResolveSecret("sk-literal-key-123")
	if err != nil || got != "sk-literal-key-123" {
		t.Fatalf("literal: got %q err %v", got, err)
	}

	got, err = ResolveSecret("price is $5")
	if err != nil || got != "price is $5" {
		t.Fatalf("embedded $: got %q err %v", got, err)
	}
}

func TestProviderHoldsReferenceNotValue(t *testing.T) {
	t.Setenv("K_BRAIN_SECRET_TEST", "resolved-value")

	p := Provider{Name: "test", BaseURL: "https://other.example.com", APIKey: "${K_BRAIN_SECRET_TEST}"}
	if p.APIKey != "${K_BRAIN_SECRET_TEST}" {
		t.Fatalf("config must hold the raw reference, got %q", p.APIKey)
	}
	k, err := p.ResolveKey()
	if err != nil || k != "resolved-value" {
		t.Fatalf("ResolveKey: got %q err %v", k, err)
	}

	p.APIKey = "$K_BRAIN_SECRET_UNSET"
	if k := p.Key(); k != "" {
		t.Fatalf("unset ref should yield empty key, got %q", k)
	}
	if _, err := p.ResolveKey(); err == nil || !strings.Contains(err.Error(), "K_BRAIN_SECRET_UNSET") {
		t.Fatalf("ResolveKey should name the unset var: %v", err)
	}

}

func TestIsWholeRef(t *testing.T) {
	whole := []string{"$FOO", "${FOO}", "${FOO:-def}", "${FOO-def}", "$F1_"}
	for _, v := range whole {
		if !IsWholeRef(v) {
			t.Errorf("IsWholeRef(%q) = false, want true", v)
		}
	}
	notWhole := []string{
		"${A}${B}",
		"${A} and ${B}",
		"$HOME/bin:$PATH",
		"$REDIS_HOST:6379",
		"postgres://$DB/db",
		"${MY KEY-x}",
		"${:-x}",
		"Bearer $TOKEN",
		"price is $5",
		"",
		"$",
	}
	for _, v := range notWhole {
		if IsWholeRef(v) {
			t.Errorf("IsWholeRef(%q) = true, want false", v)
		}
	}
}

func TestResolveSecretDefaultForms(t *testing.T) {
	t.Setenv("K_BRAIN_DEF_SET", "live")
	os.Unsetenv("K_BRAIN_DEF_UNSET")

	if got, err := ResolveSecret("${K_BRAIN_DEF_SET-fb}"); err != nil || got != "live" {
		t.Errorf("${K_BRAIN_DEF_SET-fb} = %q, %v", got, err)
	}
	if got, err := ResolveSecret("${K_BRAIN_DEF_UNSET-fb}"); err != nil || got != "fb" {
		t.Errorf("${K_BRAIN_DEF_UNSET-fb} = %q, %v", got, err)
	}

	t.Setenv("K_BRAIN_DEF_EMPTY", "")
	if got, err := ResolveSecret("${K_BRAIN_DEF_EMPTY:-fb}"); err != nil || got != "fb" {
		t.Errorf("${K_BRAIN_DEF_EMPTY:-fb} = %q, %v", got, err)
	}

	t.Setenv("MY", "my-value")
	if got, err := ResolveSecret("${MY-KEY}"); err != nil || got != "my-value" {
		t.Errorf("${MY-KEY} with MY set = %q, %v — should resolve MY", got, err)
	}

	if _, err := ExpandTemplate("${MY KEY-x}"); err == nil {
		t.Error("ExpandTemplate(${MY KEY-x}): expected invalid-name error")
	}
	if _, err := ExpandTemplate("${:-x}"); err == nil {
		t.Error("ExpandTemplate(${:-x}): expected invalid-name error")
	}
}

func TestResolveEnvMapCompoundValues(t *testing.T) {
	t.Setenv("K_BRAIN_COMPOUND_HOME", "/home/u")
	t.Setenv("K_BRAIN_COMPOUND_PATH", "/usr/bin")
	t.Setenv("K_BRAIN_COMPOUND_A", "x")
	t.Setenv("K_BRAIN_COMPOUND_B", "y")
	env, err := ResolveEnvMap(map[string]string{
		"PATHLIKE": "$K_BRAIN_COMPOUND_HOME/bin:$K_BRAIN_COMPOUND_PATH",
		"HOSTPORT": "$K_BRAIN_COMPOUND_A:6379",
		"MULTIREF": "${K_BRAIN_COMPOUND_A}${K_BRAIN_COMPOUND_B}",
	})
	if err != nil {
		t.Fatal(err)
	}
	if env["PATHLIKE"] != "/home/u/bin:/usr/bin" {
		t.Errorf("PATHLIKE = %q, want /home/u/bin:/usr/bin", env["PATHLIKE"])
	}
	if env["HOSTPORT"] != "x:6379" {
		t.Errorf("HOSTPORT = %q, want x:6379", env["HOSTPORT"])
	}
	if env["MULTIREF"] != "xy" {
		t.Errorf("MULTIREF = %q, want xy", env["MULTIREF"])
	}
}

func TestExpandTemplate(t *testing.T) {
	t.Setenv("K_BRAIN_TMPL_KEY", "tok")
	t.Setenv("K_BRAIN_TMPL_EMPTY", "")
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"no refs", "plain literal", "plain literal", false},
		{"bare ref embedded", "Bearer $K_BRAIN_TMPL_KEY", "Bearer tok", false},
		{"braced ref embedded", "Bearer ${K_BRAIN_TMPL_KEY}!", "Bearer tok!", false},
		{"default on unset", "${K_BRAIN_TMPL_UNSET:-fallback}", "fallback", false},
		{"default on empty", "${K_BRAIN_TMPL_EMPTY:-fallback}", "fallback", false},
		{"dash default keeps set-empty", "${K_BRAIN_TMPL_EMPTY-fallback}", "", false},
		{"dash default on unset", "${K_BRAIN_TMPL_UNSET-fallback}", "fallback", false},
		{"set var ignores default", "${K_BRAIN_TMPL_KEY:-fallback}", "tok", false},
		{"dollar digits literal", "price is $5", "price is $5", false},
		{"double dollar escape", "pa$$word", "pa$word", false},
		{"trailing dollar literal", "ends with $", "ends with $", false},
		{"unset without default errors", "Bearer $K_BRAIN_TMPL_UNSET", "", true},
		{"unterminated brace errors", "Bearer ${K_BRAIN_TMPL_KEY", "", true},
		{"nested default ref", "${K_BRAIN_TMPL_UNSET:-$K_BRAIN_TMPL_KEY}", "tok", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExpandTemplate(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ExpandTemplate(%q): expected error, got %q", tt.in, got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("ExpandTemplate(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
			}
		})
	}
}

func TestResolveEnvMap(t *testing.T) {
	t.Setenv("K_BRAIN_ENV_KEY", "resolved")
	env, err := ResolveEnvMap(map[string]string{
		"SET":       "$K_BRAIN_ENV_KEY",
		"MISSING":   "$K_BRAIN_ENV_UNSET",
		"TEMPLATE":  "x-$K_BRAIN_ENV_KEY-y",
		"LITERAL":   "no refs here",
		"DOLLARCAD": "pa$$word",
		"CMDFAIL":   "!exit 1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if env["SET"] != "resolved" || env["TEMPLATE"] != "x-resolved-y" || env["LITERAL"] != "no refs here" || env["DOLLARCAD"] != "pa$word" {
		t.Errorf("resolved env wrong: %v", env)
	}
	if _, ok := env["MISSING"]; ok {
		t.Error("unresolvable whole-ref must be dropped, not spawned as KEY=")
	}
	if _, ok := env["CMDFAIL"]; ok {
		t.Error("failing !cmd must be dropped")
	}
	if got, err := ResolveEnvMap(nil); err != nil || got != nil {
		t.Errorf("nil in, nil out: %v %v", got, err)
	}
}

func TestResolveHeader(t *testing.T) {
	t.Setenv("K_BRAIN_HDR_TOKEN", "bearer-tok")
	if got, err := ResolveHeader("Bearer $K_BRAIN_HDR_TOKEN"); err != nil || got != "Bearer bearer-tok" {
		t.Errorf("template header = %q, %v", got, err)
	}
	if got, err := ResolveHeader("$K_BRAIN_HDR_TOKEN"); err != nil || got != "bearer-tok" {
		t.Errorf("whole-ref header = %q, %v", got, err)
	}

	if _, err := ResolveHeader("Bearer $K_BRAIN_HDR_UNSET"); err == nil {
		t.Error("unset template ref must error")
	}

	if got, err := ResolveHeader("pa$$word"); err != nil || got != "pa$word" {
		t.Errorf("literal = %q, %v", got, err)
	}
}

func TestResolveSecretDefaultUnsetInnerVarErrors(t *testing.T) {
	t.Setenv("K_BRAIN_OUTER_UNSET", "")
	os.Unsetenv("K_BRAIN_OUTER_UNSET")
	os.Unsetenv("K_BRAIN_INNER_UNSET")
	for _, v := range []string{"${K_BRAIN_OUTER_UNSET:-$K_BRAIN_INNER_UNSET}", "${K_BRAIN_OUTER_UNSET-$K_BRAIN_INNER_UNSET}"} {
		if got, err := ResolveSecret(v); err == nil {
			t.Errorf("ResolveSecret(%q) = %q, want error for the unset inner var", v, got)
		}
	}

	t.Setenv("K_BRAIN_INNER_SET", "inner")
	if got, err := ResolveSecret("${K_BRAIN_OUTER_UNSET:-$K_BRAIN_INNER_SET}"); err != nil || got != "inner" {
		t.Errorf("default with a set inner var = %q, %v; want inner", got, err)
	}
}
