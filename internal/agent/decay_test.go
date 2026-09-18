package agent

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func asstWithCall(id, name, args string) ai.Message {
	return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{{
		ID: id, Type: "function",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: name, Arguments: args},
	}}}
}

func toolMsg(id, name, content string) ai.Message {
	return ai.Message{Role: "tool", ToolCallID: id, Name: name, Content: content}
}

func readResult(lines int) string {
	var b strings.Builder
	for i := 1; i <= lines; i++ {
		fmt.Fprintf(&b, "%d\tline %d\n", i, i)
	}
	return b.String()
}

func padHotWindow(msgs []ai.Message) []ai.Message {
	filler := ai.Message{Role: "assistant", Content: strings.Repeat("y", decayHotWindow*4+100)}
	return append(msgs, filler)
}

func TestDecaySupersededByNewerRead(t *testing.T) {
	a := &Agent{}
	a.Messages = padHotWindow([]ai.Message{
		{Role: "system", Content: "sys"},
		asstWithCall("c1", "read", `{"path":"foo.go"}`),
		toolMsg("c1", "read", readResult(100)),
		asstWithCall("c2", "read", `{"path":"foo.go"}`),
		toolMsg("c2", "read", readResult(80)),
	})

	if n := a.decay(); n != 1 {
		t.Fatalf("rewrites = %d, want 1", n)
	}
	got := a.Messages[2].Content
	want := "⟨read of foo.go superseded by newer read (80 lines)⟩"
	if got != want {
		t.Errorf("old read should collapse to a pointer\ngot:  %q\nwant: %q", got, want)
	}

	if a.Messages[4].Content != readResult(80) {
		t.Error("newest read must stay inline")
	}

	if n := a.decay(); n != 0 {
		t.Errorf("second pass should be a no-op, rewrote %d", n)
	}
}

func TestDecayNeverRewritesInsideHotWindow(t *testing.T) {

	a := &Agent{}
	a.Messages = []ai.Message{
		{Role: "system", Content: "sys"},
		asstWithCall("c1", "read", `{"path":"foo.go"}`),
		toolMsg("c1", "read", readResult(100)),
		asstWithCall("c2", "read", `{"path":"foo.go"}`),
		toolMsg("c2", "read", readResult(80)),
	}
	if n := a.decay(); n != 0 {
		t.Fatalf("hot-window content must not decay, rewrote %d", n)
	}

	a.Messages = padHotWindow(a.Messages)
	if n := a.decay(); n != 1 {
		t.Fatalf("rewrites = %d, want 1", n)
	}
	if !strings.Contains(a.Messages[2].Content, "superseded") {
		t.Errorf("old read should be superseded: %q", a.Messages[2].Content)
	}
}

func TestDecayWriteInvalidatesRead(t *testing.T) {
	a := &Agent{}
	a.Messages = padHotWindow([]ai.Message{
		{Role: "system", Content: "sys"},
		asstWithCall("c1", "read", `{"path":"foo.go"}`),
		toolMsg("c1", "read", readResult(100)),
		asstWithCall("c2", "write", `{"path":"foo.go","content":"x"}`),
		toolMsg("c2", "write", "wrote foo.go"),
	})

	if n := a.decay(); n != 1 {
		t.Fatalf("rewrites = %d, want 1", n)
	}
	got := a.Messages[2].Content
	if !strings.Contains(got, "superseded") || !strings.Contains(got, "write") {
		t.Errorf("write should supersede the read, got %q", got)
	}

	if a.Messages[4].Content != "wrote foo.go" {
		t.Error("small write result must stay inline")
	}
}

func TestDecayHotWindowProtectsRecent(t *testing.T) {
	big := strings.Repeat("x", decayMinBytes*2)
	a := &Agent{}

	a.Messages = []ai.Message{
		{Role: "system", Content: "sys"},
		asstWithCall("c1", "bash", `{"command":"go test"}`),
		toolMsg("c1", "bash", big),
	}
	if n := a.decay(); n != 0 {
		t.Errorf("hot-window content must not decay, rewrote %d", n)
	}

	a.Messages = padHotWindow(a.Messages)
	if n := a.decay(); n != 1 {
		t.Fatalf("rewrites = %d, want 1", n)
	}
	got := a.Messages[2].Content
	if !strings.HasPrefix(got, "⟨bash ") || !strings.Contains(got, "bytes") {
		t.Errorf("decayed placeholder should name tool and size, got %q", got[:80])
	}
}

func TestDecayNeverTouchesSmallResultsOrAssistant(t *testing.T) {
	big := strings.Repeat("x", decayMinBytes*2)
	a := &Agent{}
	a.Messages = padHotWindow([]ai.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q1", Authored: true},
		{Role: "assistant", Content: big},
		asstWithCall("c1", "bash", `{"command":"grep foo"}`),
		toolMsg("c1", "bash", "3 matches"),
	})
	if n := a.decay(); n != 0 {
		t.Fatalf("nothing eligible, rewrote %d", n)
	}
	if a.Messages[2].Content != big {
		t.Error("assistant message must be untouched")
	}
	if a.Messages[4].Content != "3 matches" {
		t.Error("small tool result must be untouched")
	}
}

func TestDecayKeepsSpillPath(t *testing.T) {
	spill := tools.Truncate(strings.Repeat("z", 60_000))
	a := &Agent{}
	a.Messages = padHotWindow([]ai.Message{
		{Role: "system", Content: "sys"},
		asstWithCall("c1", "read", `{"path":"big.go"}`),
		toolMsg("c1", "read", spill),
	})
	if n := a.decay(); n != 1 {
		t.Fatalf("rewrites = %d, want 1", n)
	}
	got := a.Messages[2].Content
	if !strings.Contains(got, "full output: ") {
		t.Fatalf("decayed placeholder should keep the spill path, got %q", got)
	}

	i := strings.LastIndex(got, "full output: ")
	path := strings.TrimSuffix(strings.TrimSpace(got[i+len("full output: "):]), "⟩")
	data, err := os.ReadFile(path)
	if err != nil || len(data) != 60_000 {
		t.Errorf("spill file should hold the full 60k output (len=%d, err=%v)", len(data), err)
	}
}

func TestDecayDuplicateReadsSameRegion(t *testing.T) {

	args := `{"path":"foo.go","offset":10,"limit":50}`
	a := &Agent{}
	a.Messages = padHotWindow([]ai.Message{
		{Role: "system", Content: "sys"},
		asstWithCall("c1", "read", args),
		toolMsg("c1", "read", readResult(50)),
		asstWithCall("c2", "read", args),
		toolMsg("c2", "read", readResult(50)),
	})
	if n := a.decay(); n != 1 {
		t.Fatalf("rewrites = %d, want 1", n)
	}
	if !strings.Contains(a.Messages[4].Content, "duplicate read of foo.go") {
		t.Errorf("the later copy should collapse to a duplicate pointer, got %q", a.Messages[4].Content)
	}
	if a.Messages[2].Content != readResult(50) {
		t.Error("the first copy stays inline")
	}

	if n := a.decay(); n != 0 {
		t.Errorf("second pass should be a no-op, rewrote %d", n)
	}
}

func TestDecaySameRegionDifferentContentKeepsNewest(t *testing.T) {

	args := `{"path":"foo.go","offset":10,"limit":50}`
	a := &Agent{}
	a.Messages = padHotWindow([]ai.Message{
		{Role: "system", Content: "sys"},
		asstWithCall("c1", "read", args),
		toolMsg("c1", "read", readResult(50)),
		asstWithCall("c2", "read", args),
		toolMsg("c2", "read", readResult(45)),
	})
	if n := a.decay(); n != 1 {
		t.Fatalf("rewrites = %d, want 1 (Layer-1 supersede only)", n)
	}
	if !strings.Contains(a.Messages[2].Content, "superseded") {
		t.Errorf("older read should be superseded by the newer vintage: %q", a.Messages[2].Content)
	}
	if a.Messages[4].Content != readResult(45) {
		t.Error("the newer, different read must stay inline")
	}
}

func TestSpillPathOfParsesBothMarkerShapes(t *testing.T) {
	legacy := "tail output\n[full output (60000 bytes): /tmp/k-brain-bash-1/x.log]"
	middle := "head\n... [100 bytes elided from the middle — full output (60000 bytes): /tmp/k-brain-bash-1/y.log] ...\ntail"
	if got := spillPathOf(legacy); got != "/tmp/k-brain-bash-1/x.log" {
		t.Errorf("legacy marker: %q", got)
	}
	if got := spillPathOf(middle); got != "/tmp/k-brain-bash-1/y.log" {
		t.Errorf("middle-elide marker: %q", got)
	}
	if got := spillPathOf("no marker here"); got != "" {
		t.Errorf("no marker should give empty, got %q", got)
	}
}

func TestDecayStripsColdImageParts(t *testing.T) {
	png := pngFixtureForDecay(t, 640, 480)
	img := ai.ImagePart("png", png)

	a := &Agent{}
	a.Messages = padHotWindow([]ai.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "look at this", Parts: []ai.ContentPart{{Type: "text", Text: "look at this"}, img}},
	})
	before := EstimateTokens(a.Messages)
	n := a.decay()
	if n == 0 {
		t.Fatal("image past the hot window should be stripped")
	}
	m := a.Messages[1]

	for _, p := range m.Parts {
		if p.Type == "image_url" {
			t.Fatal("image part should be replaced")
		}
	}

	placeholder := m.Content
	if !strings.HasPrefix(placeholder, "look at this\n") || !strings.Contains(placeholder, "omitted") {
		t.Fatalf("Content should keep the user's text and append the placeholder, got %q", placeholder)
	}
	if !strings.Contains(placeholder, "640×480") {
		t.Errorf("placeholder should name the pixel size, got %q", placeholder)
	}
	if !strings.Contains(placeholder, "k-brain-img-") {
		t.Errorf("placeholder should point at the spilled file, got %q", placeholder)
	}

	if len(m.Parts) != 0 {
		t.Errorf("stripped message should carry no parts, got %+v", m.Parts)
	}

	after := EstimateTokens(a.Messages)
	if before-after < 300 {
		t.Errorf("stripping should drop the estimate (before=%d after=%d)", before, after)
	}

	if n := a.decay(); n != 0 {
		t.Errorf("second decay should be a no-op, rewrote %d", n)
	}
}

func pngFixtureForDecay(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return buf.Bytes()
}

func TestDecayImageOnlyMessagesSpendHotWindow(t *testing.T) {
	big := ai.ImagePart("png", pngFixtureForDecay(t, 2000, 2000))
	a := &Agent{}
	a.Messages = []ai.Message{{Role: "system", Content: "sys"}}
	for range 6 {
		a.Messages = append(a.Messages, ai.Message{Role: "user", Parts: []ai.ContentPart{big}})
	}
	if n := a.decay(); n == 0 {
		t.Fatal("the oldest image-only message should fall past the hot window and be stripped")
	}
	if len(a.Messages[1].Parts) != 0 || !strings.Contains(a.Messages[1].Content, "omitted") {
		t.Fatalf("oldest message should now be a text placeholder in Content, got parts=%+v content=%q", a.Messages[1].Parts, a.Messages[1].Content)
	}
	last := a.Messages[len(a.Messages)-1]
	if last.Parts[0].Type != "image_url" {
		t.Fatal("the newest image must stay hot")
	}
}

func TestDecayedImageMessageRoundTripsKeepingText(t *testing.T) {
	img := ai.ImagePart("png", pngFixtureForDecay(t, 640, 480))
	m := ai.Message{Role: "user", Content: "look at this", Parts: []ai.ContentPart{{Type: "text", Text: "look at this"}, img}}
	if stripImageParts(&m) != 1 {
		t.Fatal("one image should strip")
	}
	raw, err := m.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var back ai.Message
	if err := back.UnmarshalJSON(raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(back.TextContent(), "look at this") || !strings.Contains(back.TextContent(), "omitted") {
		t.Fatalf("reload should keep the user's text and the placeholder, got %q", back.TextContent())
	}
	if strings.Count(string(raw), "omitted") != 1 {
		t.Fatalf("placeholder must be sent once on the wire, got %d in %s", strings.Count(string(raw), "omitted"), raw)
	}
}

func TestSpillImageRejectsNonDataURL(t *testing.T) {
	for _, u := range []string{"x;base64,y", ";base64,abcd", "http://example/img.png", ""} {
		if got := spillImage(u); got != "" {
			t.Errorf("spillImage(%q) = %q, want \"\"", u, got)
		}
	}
}
