package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func texts(cs []cand) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Text
	}
	return out
}

func TestCompletions(t *testing.T) {
	models := []cand{{"kimi-k3-fast", ""}, {"glm-5.2-fast", ""}}
	provs := []cand{{"inference", ""}}

	head, cs := completions("/m", models, provs, nil, nil)
	if head != "" || len(cs) != 6 || cs[0].Text != "/mcp" || cs[1].Text != "/mcps" || cs[2].Text != "/memory" || cs[3].Text != "/model" || cs[4].Text != "/model-for-session" || cs[5].Text != "/mouse" {
		t.Fatalf("command completion: %q %v", head, texts(cs))
	}

	for _, cmd := range []string{"/context-doctor", "/mcp"} {
		found := false
		for _, c := range commands {
			if c.Text == cmd {
				found = true
			}
		}
		if !found {
			t.Errorf("%s missing from completion table", cmd)
		}
	}

	head, cs = completions("/model k", models, provs, nil, nil)
	if head != "/model " || len(cs) != 1 || cs[0].Text != "kimi-k3-fast" {
		t.Fatalf("model completion: %q %v", head, texts(cs))
	}

	_, cs = completions("/model kimi-k3-fast inf", models, provs, nil, nil)
	if len(cs) != 1 || cs[0].Text != "inference" {
		t.Fatalf("provider completion: %v", texts(cs))
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "alpha.txt"), nil, 0o644)
	os.Mkdir(filepath.Join(dir, "alphadir"), 0o755)
	head, cs = completions("fix "+dir+"/al", models, provs, nil, nil)
	if head != "fix " || len(cs) != 2 {
		t.Fatalf("path completion: %q %v", head, texts(cs))
	}
	if cs[1].Text != filepath.Join(dir, "alphadir")+"/" {
		t.Fatalf("dir should get trailing slash: %v", texts(cs))
	}

	_, cs = completions("/nope", models, provs, nil, nil)
	if len(cs) != 0 {
		t.Fatalf("expected no candidates, got %v", texts(cs))
	}

	for _, in := range []string{"/goal make yourself better", "/goal make yourself better ", "/resume ab"} {
		if _, cs = completions(in, models, provs, nil, nil); len(cs) != 0 {
			t.Fatalf("%q: expected no candidates, got %v", in, texts(cs))
		}
	}

	if _, cs = completions("/goal fix @"+dir+"/al", models, provs, nil, nil); len(cs) != 2 {
		t.Fatalf("@ inside slash args: %v", texts(cs))
	}
}
