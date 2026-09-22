package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRenderAncientTextOrder(t *testing.T) {
	out := ansi.Strip(renderAncientText("天地玄黄", 20))
	lines := strings.Split(out, "\n")
	if len(lines) < 4 {
		t.Fatalf("lines = %d, output %q", len(lines), out)
	}
	var got strings.Builder
	for _, line := range lines {
		got.WriteString(strings.TrimSpace(line))
	}
	if got.String() != "天地玄黄" {
		t.Fatalf("order = %q, output %q", got.String(), out)
	}
}

func TestRenderAncientTextKeepsLatinWordsTogether(t *testing.T) {
	out := ansi.Strip(renderAncientText("hello world 你好", 40))
	if !strings.Contains(out, "hello") || !strings.Contains(out, "world") {
		t.Fatalf("latin words were split: %q", out)
	}
}

func TestAncientCommandToggles(t *testing.T) {
	m := compactCmdModel()
	m.command("/ancient on")
	if !m.ancientMode {
		t.Fatal("ancient mode did not enable")
	}
	m.command("/ancient off")
	if m.ancientMode {
		t.Fatal("ancient mode did not disable")
	}
}

func TestAncientKeepsInterfaceHorizontal(t *testing.T) {
	m := compactCmdModel()
	m.width, m.height = 120, 60
	m.input.SetWidth(118)
	m.ancientMode = true
	m.syncInputPlaceholder()
	out := ansi.Strip(m.View())
	for _, want := range []string{"k-brain", m.input.Placeholder, "Shift+Tab", "Ctrl+C", m.permissionModeLabel()} {
		if !strings.Contains(out, want) {
			t.Errorf("horizontal interface missing %q: %q", want, out)
		}
	}
	for _, kind := range []blockKind{blockText, blockTool, blockToolRun, blockToolQueued} {
		b := block{kind: kind, text: "模式：计划 /privacy on", stale: true}
		if got, want := b.renderAtMode(120, true), b.render(120); got != want {
			t.Errorf("interface block %d changed in ancient mode: %q", kind, got)
		}
	}
	for _, value := range []string{"", " ", "/ancient off", "/privacy on"} {
		m.input.SetValue(value)
		if m.ancientInput() {
			t.Errorf("placeholder or command should stay horizontal: %q", value)
		}
	}
	m.input.SetValue("天地玄黄")
	if !m.ancientInput() {
		t.Fatal("conversation input should stay vertical")
	}
}

func TestAncientModeUsesVerticalLayoutForInputAndBlocks(t *testing.T) {
	m := compactCmdModel()
	m.width, m.height = 40, 20
	m.appendAssistantBlock("天地玄黄")
	m.input.SetValue("输入")
	m.command("/ancient on")
	vertical := ansi.Strip(m.blocks[0].renderAtMode(40, true))
	verticalLines := strings.Split(vertical, "\n")
	for i := range verticalLines {
		verticalLines[i] = strings.TrimSpace(verticalLines[i])
	}
	if !strings.Contains(strings.Join(verticalLines, "\n"), "天\n地") {
		t.Fatalf("assistant output is not vertical: %q", m.blocks[0].rendered)
	}
	out := ansi.Strip(m.View())
	if !strings.Contains(out, "输") || !strings.Contains(out, "入") {
		t.Fatalf("input is not vertical: %q", out)
	}
}
