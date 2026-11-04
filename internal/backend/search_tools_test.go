package backend

import "testing"

// The backend's LiveAgent catalog brings root-confined Grep, Glob and List, so the
// CLI's unconfined grep, glob and list stay out of backend sessions, and so does
// web_fetch, which the app's tool catalog does not list.
func TestBackendSessionsUseTheCatalogSearchTools(t *testing.T) {
	store, backend, _, sess := compactFixture(t, &scriptedClient{response: "unused"})
	defer store.Close()
	rt, err := backend.loadRuntimeByID(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range rt.agent.Tools {
		names[tool.Def.Function.Name] = true
	}
	for _, want := range []string{"Grep", "Glob", "List", "read"} {
		if !names[want] {
			t.Errorf("backend session lacks %s", want)
		}
	}
	for _, dropped := range []string{"grep", "glob", "list", "web_fetch"} {
		if names[dropped] {
			t.Errorf("backend session offers the CLI's %s next to the catalog's", dropped)
		}
	}
}
