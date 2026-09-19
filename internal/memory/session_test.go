package memory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestSessionPathValidationAndDeletion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	st, err := session.OpenProjectHome(home)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.Create(t.TempDir(), "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "missing", "../escape", `..\escape`, "/absolute", "C:\\absolute"} {
		if scope := Session(bad); scope.Path != "" {
			t.Errorf("accepted %q: %+v", bad, scope)
		}
	}
	scope := Session(id)
	if err := scope.Remember("session note"); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(scope.Path); !os.IsNotExist(err) {
		t.Fatalf("memory survived deletion: %v", err)
	}
	if Session(id).Path != "" {
		t.Fatal("deleted session still resolves")
	}
}

func TestSessionPathRejectsAmbiguousID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)
	for _, project := range []string{"project-one", "project-two"} {
		dir := filepath.Join(home, "sessions", project, "same-id")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if scope := Session("same-id"); scope.Path != "" {
		t.Fatalf("ambiguous session resolved: %+v", scope)
	}
}
