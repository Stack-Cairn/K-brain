package permissions

import "testing"

// The lsp tool reads the file it queries, so it is read-only and Read path rules
// govern it.
func TestLSPToolIsAReadOfItsPath(t *testing.T) {
	if !IsReadOnlyTool("lsp") {
		t.Fatal("lsp must be read-only")
	}
	root := t.TempDir()
	opts := Options{ProjectRoot: root, AllowReadOnly: true}
	if d := Evaluate(Request{Tool: "lsp", Command: root + "/secret/key.go"}, opts, deny("Read(secret/**)")); d.Effect != Deny {
		t.Fatalf("Read deny rule must cover lsp: %+v", d)
	}
	if d := Evaluate(Request{Tool: "lsp", Command: root + "/main.go"}, opts, deny("Read(secret/**)")); d.Effect != Allow || d.Source != SourceReadOnly {
		t.Fatalf("lsp outside the denied path should be allowed as read-only: %+v", d)
	}
}
