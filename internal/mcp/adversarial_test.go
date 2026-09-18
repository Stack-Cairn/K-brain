package mcp

import "testing"

func TestTOMLEscapedQuoteBeforeHash(t *testing.T) {
	cfgs, err := ParseCodex([]byte("[mcp_servers.x]\nargs = [\"a\\\"#b\", \"c\"]\ncommand = \"s\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfgs["x"].Command; len(got) != 3 || got[1] != `a"#b` || got[2] != "c" {
		t.Errorf("args = %v", got)
	}
}

func TestTOMLTrailingEscapedBackslash(t *testing.T) {
	cfgs, err := ParseCodex([]byte("[mcp_servers.x]\ncommand = \"s\"\nargs = [\"dir\\\\\", \"x\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfgs["x"].Command; len(got) != 3 || got[1] != `dir\` || got[2] != "x" {
		t.Errorf("args = %v", got)
	}
}

func TestTOMLServerNamedFooEnv(t *testing.T) {
	cfgs, err := ParseCodex([]byte("[mcp_servers.\"foo.env\"]\ncommand = \"s\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfgs["foo.env"]; !ok {
		t.Error("server named foo.env must not be folded away")
	}
}

func TestTOMLEnvInlinePlusSubTableMerge(t *testing.T) {
	cfgs, err := ParseCodex([]byte("[mcp_servers.x]\ncommand = \"s\"\nenv = { INLINE = \"1\" }\n[mcp_servers.x.env]\nSUB = \"2\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfgs["x"].Env["INLINE"] != "1" || cfgs["x"].Env["SUB"] != "2" {
		t.Errorf("env = %v", cfgs["x"].Env)
	}
}

func TestTOMLHashInPlainString(t *testing.T) {
	cfgs, err := ParseCodex([]byte("[mcp_servers.x]\ncommand = \"s\"\nurl = \"http://h/#frag\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfgs["x"].URL != "http://h/#frag" {
		t.Errorf("url = %q", cfgs["x"].URL)
	}
}
