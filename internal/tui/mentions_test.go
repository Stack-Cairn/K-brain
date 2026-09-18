package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/skills"
)

func TestExpandMentions(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "main.go")
	os.WriteFile(f, []byte("x"), 0o644)

	out := expandMentions("look at @" + f + "#10-40, please")
	if !strings.Contains(out, f+" (lines 10-40)") || !strings.Contains(out, "not inlined") {
		t.Fatalf("absolute+range: %q", out)
	}

	out = expandMentions("check @" + f + "#7")
	if !strings.Contains(out, "(lines 7)") {
		t.Fatalf("single line: %q", out)
	}

	t.Chdir(dir)
	out = expandMentions("see @main.go here")
	if !strings.Contains(out, f) {
		t.Fatalf("relative: %q", out)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	os.WriteFile(filepath.Join(home, "notes.md"), []byte("x"), 0o644)
	out = expandMentions("read @~/notes.md")
	if !strings.Contains(out, filepath.Join(home, "notes.md")) {
		t.Fatalf("tilde: %q", out)
	}

	for _, in := range []string{"email me @ 5pm", "ping @nonexistent-file-xyz", "no mentions at all"} {
		if got := expandMentions(in); got != in {
			t.Fatalf("should be unchanged: %q -> %q", in, got)
		}
	}
}

func TestAtMentionCompletion(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "alpha.txt"), nil, 0o644)
	_, cs := completions("fix @"+dir+"/al", nil, nil, nil, nil)
	if len(cs) != 1 || cs[0].Text != "@"+filepath.Join(dir, "alpha.txt") {
		t.Fatalf("@ completion: %v", texts(cs))
	}
}

func TestExpandSkills(t *testing.T) {
	sk := []skills.Skill{{Name: "go-style", Description: "d", Path: "/x/go-style/SKILL.md"}}
	out := expandSkills("apply $go-style, thanks", sk)
	if !strings.Contains(out, "go-style (/x/go-style/SKILL.md)") || !strings.Contains(out, "follow its instructions") {
		t.Fatalf("skill note: %q", out)
	}
	for _, in := range []string{"costs $5 now", "run $unknown-skill", "no tokens"} {
		if got := expandSkills(in, sk); got != in {
			t.Fatalf("should be unchanged: %q -> %q", in, got)
		}
	}
}

func TestSkillCompletion(t *testing.T) {
	sk := []cand{{"$go-style", "style rules"}, {"$go-testing", "test rules"}, {"$other", ""}}
	_, cs := completions("apply $go-", nil, nil, sk, nil)
	if len(cs) != 2 || cs[0].Text != "$go-style" && cs[1].Text != "$go-testing" {
		t.Fatalf("$ completion: %v", texts(cs))
	}
}

func TestSkillCompletionAfterNewline(t *testing.T) {
	sk := []cand{{"$go-style", "style rules"}, {"$go-testing", "test rules"}, {"$other", ""}}
	for _, val := range []string{
		"first line\n$go-",
		"first line\napply $go-",
		"pasted\nmulti\nline\nuse $g",
	} {
		head, cs := completions(val, nil, nil, sk, nil)
		if len(cs) == 0 || !strings.HasPrefix(cs[0].Text, "$go-") {
			t.Fatalf("$ completion after newline: %q -> %v", val, texts(cs))
		}
		if !strings.HasSuffix(head, "$") && !strings.HasSuffix(head, " ") && !strings.HasSuffix(head, "\n") {
			t.Fatalf("head should keep everything before the token: %q -> head %q", val, head)
		}
	}
}

func TestAtMentionCompletionAfterNewline(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "alpha.txt"), nil, 0o644)
	_, cs := completions("first line\nfix @"+dir+"/al", nil, nil, nil, nil)
	if len(cs) != 1 || cs[0].Text != "@"+filepath.Join(dir, "alpha.txt") {
		t.Fatalf("@ completion after newline: %v", texts(cs))
	}
}

func TestPrepareTurnReloadsSkillsEveryTurn(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))

	os.MkdirAll(filepath.Join(dir, ".agents/skills/demo"), 0o755)
	os.WriteFile(filepath.Join(dir, ".agents/skills/demo/SKILL.md"),
		[]byte("---\nname: demo\ndescription: live demo skill\n---\n"), 0o644)

	m := &model{agent: agent.New(ai.New("http://unused", "k"), "m", 1, "overwritten"), sysPrompt: "BASE"}
	out, _ := m.prepareTurn("use $demo now")
	sys := m.agent.Messages[0].Content
	if !strings.HasPrefix(sys, "BASE") || !strings.Contains(sys, "<name>demo</name>") || !strings.Contains(sys, "<description>live demo skill</description>") {
		t.Fatalf("system prompt: %q", sys)
	}
	if !strings.Contains(out, "invoked skill(s): demo") {
		t.Fatalf("expansion: %q", out)
	}

	os.MkdirAll(filepath.Join(dir, ".agents/skills/fresh"), 0o755)
	os.WriteFile(filepath.Join(dir, ".agents/skills/fresh/SKILL.md"),
		[]byte("---\nname: fresh\ndescription: added mid-session\n---\n"), 0o644)
	m.prepareTurn("hello")
	if !strings.Contains(m.agent.Messages[0].Content, "<name>fresh</name>") {
		t.Fatalf("new skill not picked up: %q", m.agent.Messages[0].Content)
	}
}

func TestImageParts(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(img, []byte("\x89PNG\r\n\x1a\nfake"), 0o644); err != nil {
		t.Fatal(err)
	}

	parts, note := imageParts("what is in @" + img + " ?")
	if len(parts) != 1 {
		t.Fatalf("expected 1 image part, got %d", len(parts))
	}
	p := parts[0]
	if p.Type != "image_url" || p.ImageURL == nil {
		t.Fatalf("part: %+v", p)
	}
	if !strings.HasPrefix(p.ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("data url: %q", p.ImageURL.URL)
	}
	if !strings.Contains(note, "attached image(s):") || !strings.Contains(note, img) {
		t.Fatalf("note: %q", note)
	}

	if parts, note := imageParts("see @foo.go"); parts != nil || note != "" {
		t.Fatalf("text tag should not produce image parts: %v %q", parts, note)
	}
}

func TestMessageMultimodalRoundTrip(t *testing.T) {
	parts := []ai.ContentPart{ai.ImagePart("png", []byte("fake-image-bytes"))}
	m := ai.Message{Role: "user", Content: "describe", Parts: parts}

	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(data), `"type":"image_url"`) {
		t.Fatalf("marshaled: %s", data)
	}

	var back ai.Message
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.TextContent() != "describe" {
		t.Fatalf("text content: %q", back.TextContent())
	}
	if len(back.Parts) != 1 || back.Parts[0].ImageURL == nil {
		t.Fatalf("parts: %+v", back.Parts)
	}
}

func TestSupportsVisionGate(t *testing.T) {
	newModel := func(visionCfg bool, catalog *config.Catalog) *model {
		ag := agent.New(ai.New("http://unused", "k"), "m", 1, "sys")
		ag.Model = "some-model"
		m := &model{
			agent:     ag,
			modelName: "some-model",
			provName:  "inference",
			cfg:       &config.Config{Models: map[string]config.Model{"some-model": {Vision: visionCfg}}},
			catalogs:  map[string]config.Catalog{},
		}
		if catalog != nil {
			m.catalogs["inference"] = *catalog
		}
		return m
	}

	if newModel(false, nil).supportsVision() {
		t.Error("config vision=false should not enable vision")
	}

	if !newModel(true, nil).supportsVision() {
		t.Error("config vision=true should enable vision")
	}

	withImg := &config.Catalog{Models: []config.ModelInfoLite{{ID: "some-model", InputModalities: []string{"text", "image"}}}}
	if !newModel(false, withImg).supportsVision() {
		t.Error("provider-advertised image modality should override config vision=false")
	}

	textOnly := &config.Catalog{Models: []config.ModelInfoLite{{ID: "some-model", InputModalities: []string{"text"}}}}
	if newModel(true, textOnly).supportsVision() {
		t.Error("provider-advertised text-only should override config vision=true")
	}

	noModal := &config.Catalog{Models: []config.ModelInfoLite{{ID: "some-model"}}}
	if !newModel(true, noModal).supportsVision() {
		t.Error("no advertised modalities should fall back to config vision=true")
	}
}

func TestPrepareTurnVisionGate(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(img, []byte("\x89PNG\r\n\x1a\nfake"), 0o644); err != nil {
		t.Fatal(err)
	}
	build := func(vision bool) *model {
		ag := agent.New(ai.New("http://unused", "k"), "m", 1, "sys")
		ag.Model = "m"
		return &model{
			agent:     ag,
			modelName: "m",
			provName:  "p",
			cfg:       &config.Config{Models: map[string]config.Model{"m": {Vision: vision}}},
			catalogs:  map[string]config.Catalog{},
		}
	}

	_, parts := build(false).prepareTurn("look @" + img)
	if len(parts) != 0 {
		t.Errorf("text-only model should get no image parts, got %d", len(parts))
	}
	txt, parts := build(true).prepareTurn("look @" + img)
	if len(parts) != 1 {
		t.Fatalf("vision model should get 1 image part, got %d", len(parts))
	}
	if !strings.Contains(txt, "attached image(s):") {
		t.Errorf("vision note missing: %q", txt)
	}
}
