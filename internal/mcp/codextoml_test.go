package mcp

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestParseTOMLValue(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want any
	}{
		{`true`, true},
		{`false`, false},
		{`42`, int64(42)},
		{`-7`, int64(-7)},
		{`1.5`, 1.5},
		{`"plain"`, "plain"},
		{`""`, ""},
		{`"a\nb\tc\rd\\e\"f"`, "a\nb\tc\rd\\e\"f"},
		{`'lit\nno-escape'`, `lit\nno-escape`},
		{`''`, ""},
		{`[]`, []string(nil)},
		{`["a", "b"]`, []string{"a", "b"}},
		{`["a, b"]`, []string{"a, b"}},
		{`{}`, map[string]any{}},
		{`{ a = "1", b = 2, c = true }`, map[string]any{"a": "1", "b": int64(2), "c": true}},
		{`{outer = {inner = "x"}, after = "y"}`, map[string]any{"outer": map[string]any{"inner": "x"}, "after": "y"}},
		{`{"quoted key" = "v"}`, map[string]any{"quoted key": "v"}},
	} {
		got, err := parseTOMLValue(tc.in)
		if err != nil {
			t.Errorf("parseTOMLValue(%s) error: %v", tc.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseTOMLValue(%s) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}

func TestParseTOMLValueErrors(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{``, "unsupported value"},
		{`nope`, "unsupported value"},
		{`"`, "unterminated string"},
		{`'`, "unterminated string"},
		{`'abc`, "unterminated string"},
		{`"abc`, "unterminated string"},
		{`"a"b"`, "trailing data after string"},
		{`"a\`, "unterminated escape"},
		{`"a\q"`, `unsupported escape \q`},
		{`[1, 2`, "unterminated array"},
		{`[nope]`, "unsupported value"},
		{`[["x"]]`, "array elements must be strings"},
		{`{a = "1"`, "unterminated inline table"},
		{`{a}`, "expected key = value in inline table"},
		{`{a = nope}`, "unsupported value"},
	} {
		got, err := parseTOMLValue(tc.in)
		if err == nil {
			t.Errorf("parseTOMLValue(%s) = %#v, want error %q", tc.in, got, tc.want)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("parseTOMLValue(%s) error = %q, want it to contain %q", tc.in, err, tc.want)
		}
	}
}

func TestParseCodexScalarKeys(t *testing.T) {
	cfgs, err := ParseCodex([]byte(`
[mcp_servers.x]
command = "srv"
cwd = "/srv/root"
enabled = true
startup_timeout_ms = 1500
tool_timeout_sec = 90
unknown_codex_key = "ignored"
`))
	if err != nil {
		t.Fatal(err)
	}
	x := cfgs["x"]
	if x.Cwd != "/srv/root" {
		t.Errorf("cwd = %q", x.Cwd)
	}
	if x.Enabled == nil || !*x.Enabled || x.Disabled() {
		t.Errorf("enabled = %v", x.Enabled)
	}
	if x.StartupTimeout != 2 {
		t.Errorf("startup_timeout_ms 1500 → %ds, want 2s", x.StartupTimeout)
	}
	if x.ToolTimeout != 90 {
		t.Errorf("tool_timeout_sec = %d", x.ToolTimeout)
	}
}

func TestParseCodexTypeErrors(t *testing.T) {
	for _, tc := range []struct{ doc, want string }{
		{"[mcp_servers.x]\nargs = \"notanarray\"\n", "args must be an array of strings"},
		{"[mcp_servers.x]\nenv = \"notatable\"\n", "env must be an inline table"},
		{"[mcp_servers.x]\nenvironment = 3\n", "environment must be an inline table"},
		{"[mcp_servers.x]\nheaders = 3\n", "headers must be an inline table"},
		{"[mcp_servers.x]\nurl = 3\n", "url must be a string"},
		{"[mcp_servers.x]\ncwd = 3\n", "cwd must be a string"},
		{"[mcp_servers.x]\nstartup_timeout_sec = \"soon\"\n", "startup_timeout_sec must be an integer"},
		{"[mcp_servers.x]\nstartup_timeout_ms = \"soon\"\n", "startup_timeout_ms must be an integer"},
		{"[mcp_servers.x]\ntool_timeout_sec = true\n", "tool_timeout_sec must be an integer"},
		{"[mcp_servers.x]\njust-a-bare-key\n", "expected key = value"},
		{"[mcp_servers.x]\ncommand = \"a\nargs = [\"b\"]\n", "unterminated string"},
	} {
		cfgs, err := ParseCodex([]byte(tc.doc))
		if err == nil {
			t.Errorf("ParseCodex(%q) = %v, want error %q", tc.doc, cfgs, tc.want)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ParseCodex(%q) error = %q, want it to contain %q", tc.doc, err, tc.want)
		}
	}
}

func TestParseCodexToolApprovalTables(t *testing.T) {
	cfgs, err := ParseCodex([]byte(`
[mcp_servers.incident_io]
url = "https://mcp.incident.io/mcp"

[mcp_servers.incident_io.tools.ask_telemetry]
approval_mode = "approve"

[mcp_servers.inf-memory]
url = "http://office/api/mcp"
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 2 {
		t.Fatalf("got %d servers %v, want 2", len(cfgs), keys(cfgs))
	}
	if cfgs["incident_io"].URL != "https://mcp.incident.io/mcp" {
		t.Errorf("incident_io url = %q", cfgs["incident_io"].URL)
	}
	for name := range cfgs {
		if strings.Contains(name, ".") {
			t.Errorf("bogus sub-table server leaked into configs: %q", name)
		}
	}
}

func TestParseCodexServerNamedTools(t *testing.T) {
	cfgs, err := ParseCodex([]byte("[mcp_servers.x.tools]\ncommand = \"srv\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfgs["x.tools"].Command, []string{"srv"}) {
		t.Errorf("x.tools command = %v", cfgs["x.tools"].Command)
	}
}

func TestParseCodexHTTPHeadersAndBearer(t *testing.T) {
	cfgs, err := ParseCodex([]byte(`
[mcp_servers.a]
url = "https://a.example/mcp"
http_headers = { "X-Team" = "eng" }
bearer_token_env_var = "K_BRAIN_TEST_TOKEN"

[mcp_servers.b]
url = "https://b.example/mcp"
headers = { "Authorization" = "Bearer explicit" }
bearer_token_env_var = "K_BRAIN_TEST_TOKEN"
`))
	if err != nil {
		t.Fatal(err)
	}

	if got := cfgs["a"].Headers["Authorization"]; got != "Bearer $K_BRAIN_TEST_TOKEN" {
		t.Errorf("a Authorization = %q, want the lazy $VAR reference", got)
	}
	if got := cfgs["a"].Headers["X-Team"]; got != "eng" {
		t.Errorf("a X-Team = %q", got)
	}
	if got := cfgs["b"].Headers["Authorization"]; got != "Bearer explicit" {
		t.Errorf("b Authorization = %q, explicit literal header must win", got)
	}
}

func keys(m map[string]ServerConfig) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestLoadCodex(t *testing.T) {

	if _, err := LoadCodex(""); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("LoadCodex(\"\") = %v, want os.ErrNotExist", err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[mcp_servers.d]\ncommand = \"srv\"\nargs = ['--stdio']\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgs, err := LoadCodex(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfgs["d"].Command, []string{"srv", "--stdio"}) {
		t.Errorf("d command = %v", cfgs["d"].Command)
	}
	if _, err := LoadCodex(filepath.Join(t.TempDir(), "missing.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file = %v, want os.ErrNotExist", err)
	}
}

func TestParseCodexAuthFields(t *testing.T) {
	t.Setenv("CIO_TEST_TOKEN", "cio-live-token")
	cfgs, err := ParseCodex([]byte(`
[mcp_servers.customerio]
url = "https://mcp.customer.io/mcp"
bearer_token_env_var = "CIO_TEST_TOKEN"

[mcp_servers.mixed]
url = "https://mcp.example.com/mcp"
http_headers = { X-Team = "eng" }
env_http_headers = { X-Api-Key = "CIO_TEST_TOKEN" }

[mcp_servers.mixedsub]
url = "https://sub.example.com/mcp"
[mcp_servers.mixedsub.http_headers]
X-Region = "us"
[mcp_servers.mixedsub.env_http_headers]
Authorization = "CIO_TEST_TOKEN"

[mcp_servers.collide_inline]
url = "https://ci.example.com/mcp"
http_headers = { Authorization = "Bearer literal" }
env_http_headers = { Authorization = "CIO_TEST_TOKEN" }

[mcp_servers.collide_sub]
url = "https://cs.example.com/mcp"
[mcp_servers.collide_sub.http_headers]
Authorization = "Bearer literal"
[mcp_servers.collide_sub.env_http_headers]
Authorization = "CIO_TEST_TOKEN"

[mcp_servers.collide_bearer]
url = "https://cb.example.com/mcp"
http_headers = { Authorization = "Bearer literal" }
bearer_token_env_var = "CIO_TEST_TOKEN"
`))
	if err != nil {
		t.Fatal(err)
	}
	cio := cfgs["customerio"]
	if got := cio.Headers["Authorization"]; got != "Bearer $CIO_TEST_TOKEN" {
		t.Fatalf("bearer_token_env_var should import as a reference, got %q", got)
	}

	hv, err := config.ResolveHeader(cio.Headers["Authorization"])
	if err != nil || hv != "Bearer cio-live-token" {
		t.Errorf("connect-time resolution = %q, %v", hv, err)
	}
	mixed := cfgs["mixed"]
	if mixed.Headers["X-Team"] != "eng" || mixed.Headers["X-Api-Key"] != "$CIO_TEST_TOKEN" {
		t.Errorf("inline http_headers/env_http_headers = %v", mixed.Headers)
	}
	sub := cfgs["mixedsub"]
	if sub.Headers["X-Region"] != "us" || sub.Headers["Authorization"] != "$CIO_TEST_TOKEN" {
		t.Errorf("sub-table http_headers/env_http_headers = %v", sub.Headers)
	}

	for _, name := range []string{"collide_inline", "collide_sub", "collide_bearer"} {
		if got := cfgs[name].Headers["Authorization"]; got != "Bearer literal" {
			t.Errorf("%s: literal should win on collision, got %q", name, got)
		}
	}
}

func TestParseCodexHeaderPrecedence(t *testing.T) {
	doc := []byte(`
[mcp_servers.inline]
url = "https://i.example.com/mcp"
http_headers = { Authorization = "Bearer lit", X-Literal = "v" }
env_http_headers = { Authorization = "AUTH_VAR", X-Env = "ENV_VAR" }
bearer_token_env_var = "BEARER_VAR"

[mcp_servers.sub]
url = "https://s.example.com/mcp"
[mcp_servers.sub.http_headers]
Authorization = "Bearer lit"
X-Literal = "v"
[mcp_servers.sub.env_http_headers]
Authorization = "AUTH_VAR"
X-Env = "ENV_VAR"
`)
	cfgs, err := ParseCodex(doc)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Authorization": "Bearer lit",
		"X-Literal":     "v",
		"X-Env":         "$ENV_VAR",
	}
	for _, name := range []string{"inline", "sub"} {
		got := cfgs[name].Headers
		if len(got) != len(want) {
			t.Errorf("%s: header count = %d, want %d (%v)", name, len(got), len(want), got)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: header %s = %q, want %q", name, k, got[k], v)
			}
		}

		if got["X-Literal"] != "v" {
			t.Errorf("%s: literal lost %v", name, got)
		}
	}

	bcfgs, err := ParseCodex([]byte(`
[mcp_servers.bearer]
url = "https://b.example.com/mcp"
bearer_token_env_var = "BEARER_VAR"
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := bcfgs["bearer"].Headers["Authorization"]; got != "Bearer $BEARER_VAR" {
		t.Errorf("bearer_token_env_var = %q, want %q", got, "Bearer $BEARER_VAR")
	}

	inline := cfgs["inline"].Headers
	sub := cfgs["sub"].Headers
	if len(inline) != len(sub) {
		t.Fatalf("inline/sub header counts differ: %v vs %v", inline, sub)
	}
	for k, v := range inline {
		if sub[k] != v {
			t.Errorf("inline/sub mismatch on %s: %q vs %q", k, v, sub[k])
		}
	}
}

func TestParseCodexPreservesReferences(t *testing.T) {
	os.Unsetenv("K_BRAIN_IMPORT_LATE_VAR")
	doc := []byte(`
[mcp_servers.stdio]
command = "srv"
env = { API_KEY = "$K_BRAIN_IMPORT_LATE_VAR", REGION = "us1" }

[mcp_servers.remote]
url = "https://mcp.example.com/mcp"
headers = { Authorization = "Bearer ${K_BRAIN_IMPORT_LATE_VAR:-none}" }
`)
	cfgs, err := ParseCodex(doc)
	if err != nil {
		t.Fatal(err)
	}
	if cfgs["stdio"].Env["API_KEY"] != "$K_BRAIN_IMPORT_LATE_VAR" {
		t.Errorf("env ref baked at parse: %q", cfgs["stdio"].Env["API_KEY"])
	}
	if cfgs["remote"].Headers["Authorization"] != "Bearer ${K_BRAIN_IMPORT_LATE_VAR:-none}" {
		t.Errorf("header ref baked at parse: %q", cfgs["remote"].Headers["Authorization"])
	}

	env, err := config.ResolveEnvMap(cfgs["stdio"].Env)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := env["API_KEY"]; ok || env["REGION"] != "us1" {
		t.Errorf("spawn env = %v", env)
	}

	t.Setenv("K_BRAIN_IMPORT_LATE_VAR", "late-value")
	env, err = config.ResolveEnvMap(cfgs["stdio"].Env)
	if err != nil || env["API_KEY"] != "late-value" {
		t.Errorf("late resolution = %v, %v", env, err)
	}
	if hv, err := config.ResolveHeader(cfgs["remote"].Headers["Authorization"]); err != nil || hv != "Bearer late-value" {
		t.Errorf("header resolution = %q, %v", hv, err)
	}
}

func TestParseCodexBearerTokenEnvVarInvalid(t *testing.T) {
	if _, err := ParseCodex([]byte("[mcp_servers.x]\nurl = \"http://x\"\nbearer_token_env_var = 42\n")); err == nil {
		t.Error("non-string bearer_token_env_var should error")
	}
}
