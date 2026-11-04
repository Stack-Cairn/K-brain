package permissions

import "testing"

// The CLI's grep, glob and list share the LiveAgent Grep, Glob and List rules and are
// waved through like read.
func TestCLISearchToolsAreReadOnlyPathTools(t *testing.T) {
	ro := Options{ProjectRoot: "/p", AllowReadOnly: true}
	for _, tool := range []string{"grep", "glob", "list"} {
		if !IsReadOnlyTool(tool) {
			t.Errorf("%s should be read-only", tool)
		}
		if d := Evaluate(Request{Tool: tool, Command: "/p/src"}, ro); d.Effect != Allow || d.Source != SourceReadOnly {
			t.Errorf("%s under the read-only allowance = %+v, want allow", tool, d)
		}
		if d := Evaluate(Request{Tool: tool, Command: "/p/src"}, Options{ProjectRoot: "/p"}); d.Effect != "" {
			t.Errorf("%s without the allowance = %+v, want undecided", tool, d)
		}
	}
	for tool, rule := range map[string]string{"grep": "Grep(/p/secret/**)", "glob": "Glob(/p/secret/**)", "list": "List(/p/secret/**)"} {
		if d := Evaluate(Request{Tool: tool, Command: "/p/secret/keys"}, ro, deny(rule)); d.Effect != Deny {
			t.Errorf("%s should honor the deny rule %s, got %+v", tool, rule, d)
		}
	}
}
