package sandbox

import "testing"

func TestPolicyForRootCopiesConfiguration(t *testing.T) {
	p := &Policy{Mode: "strict", Backend: "auto", Root: "old", Network: true, Writable: []string{"cache"}, ReadOnly: []string{"source"}}
	got := p.ForRoot("new")
	if got == p || got.Root != "new" || p.Root != "old" || got.Mode != p.Mode || got.Backend != p.Backend || got.Network != p.Network {
		t.Fatalf("unexpected rebased policy: %+v, original: %+v", got, p)
	}
	got.Writable[0], got.ReadOnly[0] = "changed", "changed"
	if p.Writable[0] != "cache" || p.ReadOnly[0] != "source" {
		t.Fatal("rebased policy shares path slices")
	}
}

func TestNilPolicyForRoot(t *testing.T) {
	var p *Policy
	if p.ForRoot("new") != nil {
		t.Fatal("rebasing a missing policy should not enable sandboxing")
	}
}
