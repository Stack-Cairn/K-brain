package tui

import (
	"strings"
	"testing"
)

func TestRegistryEntriesDispatch(t *testing.T) {
	for _, e := range slashRegistry() {
		if !compactCmdModel().dispatches(e.Name) {
			t.Errorf("%s is in the registry but the command switch doesn't handle it", e.Name)
		}
	}
}

func TestHelpContainsEveryRegistryHint(t *testing.T) {
	help := helpText()
	for _, e := range registry {
		if !strings.Contains(help, e.Hint) {
			t.Errorf("/help missing hint for %s: %q", e.Name, e.Hint)
		}
		if !strings.Contains(help, e.Name) {
			t.Errorf("/help missing command name %s", e.Name)
		}
	}

	m := compactCmdModel()
	m.command("/help")
	if !strings.Contains(m.blocks[len(m.blocks)-1].text, "/compact") {
		t.Fatalf("/help output missing registry content: %q", m.blocks[len(m.blocks)-1].text)
	}
}

func TestPaletteListsRegistryCommands(t *testing.T) {
	m := compactCmdModel()
	m.openPalette()
	rows := 0
	for _, it := range m.palette.all {
		if it.dynHint == nil || it.dynDesc == nil {
			continue
		}
		hint := it.dynHint(m)
		if !strings.HasPrefix(hint, "/") || strings.ContainsAny(hint, " ·<") {
			continue
		}
		e := registryFind(hint)
		if e == nil {
			t.Errorf("palette row %q hints %q, which is not in the registry", it.title, hint)
			continue
		}
		if !strings.Contains(it.dynDesc(m), e.Hint) {
			t.Errorf("palette row %q desc %q doesn't come from the registry hint %q", it.title, it.dynDesc(m), e.Hint)
		}
		rows++
	}
	if rows < 8 {
		t.Fatalf("expected the palette to surface registry commands, found %d rows", rows)
	}
}

func TestCompletionMatchesRegistry(t *testing.T) {
	slash := slashRegistry()
	if len(commands) != len(slash) {
		t.Fatalf("completion table has %d entries, registry has %d slash commands", len(commands), len(slash))
	}
	for _, e := range slash {
		found := false
		for _, c := range commands {
			if c.Text == e.Name {
				found = true
				if c.Desc != e.Hint {
					t.Errorf("completion desc for %s = %q, registry hint is %q", e.Name, c.Desc, e.Hint)
				}
			}
		}
		if !found {
			t.Errorf("%s missing from the completion table", e.Name)
		}
	}
}
