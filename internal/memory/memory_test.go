package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestScopeRoundTrip(t *testing.T) {
	s := Scope{Path: filepath.Join(t.TempDir(), "memory.md"), Name: "installation"}

	if got := s.Entries(); got != nil {
		t.Fatalf("missing file should be an empty list, got %v", got)
	}
	if err := s.Remember("prefers pnpm over npm"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remember("deploy with ./scripts/ship.sh"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(s.Path)
	if string(data) != "- [ ] prefers pnpm over npm\n- [ ] deploy with ./scripts/ship.sh\n" {
		t.Fatalf("file must be plain markdown bullets:\n%s", data)
	}

	if err := s.Forget(1); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(s.Path)
	if !strings.Contains(string(data), "- [x] prefers pnpm over npm") {
		t.Fatalf("forget should strike, not delete:\n%s", data)
	}
	if err := s.Forget(1); err == nil {
		t.Fatal("forgetting a done entry should say so")
	}
	if err := s.Forget(9); err == nil {
		t.Fatal("forgetting a missing entry should error")
	}

	block := PromptBlock(s)
	if strings.Contains(block, "pnpm") {
		t.Fatalf("done entries must not be injected:\n%s", block)
	}
	if !strings.Contains(block, "2. deploy with ./scripts/ship.sh") {
		t.Fatalf("open entry should be injected with its number:\n%s", block)
	}

	if b := PromptBlock(Scope{}, Scope{}); b != "" {
		t.Fatalf("no scopes should inject nothing, got %q", b)
	}
}

func TestRememberValidationAndCap(t *testing.T) {
	s := Scope{Path: filepath.Join(t.TempDir(), "m.md"), Name: "session"}
	if err := s.Remember("   "); err == nil {
		t.Fatal("blank entries should be rejected")
	}
	if err := s.Remember(strings.Repeat("x", maxEntryLength+1)); err == nil {
		t.Fatal("overlong entries should be rejected")
	}
	for range maxEntries {
		if err := s.Remember("fact"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Remember("one too many"); err == nil {
		t.Fatal("the cap must reject; the model is told to forget first")
	}

	if err := s.Forget(1); err != nil {
		t.Fatal(err)
	}
	if err := s.Remember("fits now"); err != nil {
		t.Fatal(err)
	}
}

func TestForgetPreservesUserProse(t *testing.T) {
	s := Scope{Path: filepath.Join(t.TempDir(), "m.md"), Name: "session"}
	os.WriteFile(s.Path, []byte("# my notes\nremember to water the plants\n- [ ] prefers dark mode\n"), 0o644)
	if err := s.Forget(1); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(s.Path)
	if !strings.Contains(string(data), "# my notes") || !strings.Contains(string(data), "water the plants") {
		t.Fatalf("hand-written prose must survive:\n%s", data)
	}
}

func TestScopeConstructors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)

	inst := Installation()
	if inst.Path != filepath.Join(home, "memory.md") || inst.Name != "installation" {
		t.Fatalf("Installation() = %+v", inst)
	}
	store, err := session.OpenProjectHome(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id, err := store.Create(t.TempDir(), "model", "provider")
	if err != nil {
		t.Fatal(err)
	}
	sess := Session(id)
	if sess.Path != filepath.Join(filepath.Dir(store.TranscriptPath(id)), "memory.md") || sess.Name != "session" {
		t.Fatalf("Session() = %+v", sess)
	}

	zero := Session("")
	if zero != (Scope{}) {
		t.Fatalf("Session(\"\") = %+v, want zero scope", zero)
	}
	if got := zero.Entries(); got != nil {
		t.Fatalf("zero scope should read empty, got %v", got)
	}
	if err := zero.Remember("x"); err == nil {
		t.Fatal("remember on the zero scope should error")
	}
	if err := zero.Forget(1); err == nil {
		t.Fatal("forget on the zero scope should error")
	}

	if err := inst.Remember("a fact"); err != nil {
		t.Fatal(err)
	}
	if es := Installation().Entries(); len(es) != 1 || es[0].Text != "a fact" {
		t.Fatalf("entries via constructor: %+v", es)
	}
}

func TestForgetMissingFile(t *testing.T) {
	s := Scope{Path: filepath.Join(t.TempDir(), "none.md"), Name: "session"}
	if err := s.Forget(1); err == nil {
		t.Fatal("forget with no file should error")
	}
}
