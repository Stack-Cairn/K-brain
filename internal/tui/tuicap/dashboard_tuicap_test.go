//go:build tuicap

package tuicap_test

import (
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/tui/tuicap"
)

// /dashboard lists the sessions stored in the throwaway home: the ones of
// this workspace first, then the others; the filter narrows the list, Tab
// groups it by workspace and Esc closes it.
func TestDashboardListsSeededSessions(t *testing.T) {
	s := tuicap.Start(t, tuicap.Options{
		Width: 110, Height: 32,
		Setup: func(home, dir string) {
			st, err := session.OpenProjectHome(home)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			for _, seed := range []struct{ cwd, title string }{
				{"/srv/api-gateway", "Gateway rate limiter"},
				{"/srv/web", "Console styling pass"},
				{dir, "Parser rewrite here"},
			} {
				id, err := st.Create(seed.cwd, "m", "p")
				if err != nil {
					t.Fatal(err)
				}
				msgs := []ai.Message{{Role: "user", Content: seed.title, Authored: true}}
				if err := st.Save(id, 0, msgs, "m", "p"); err != nil {
					t.Fatal(err)
				}
				if err := st.SetTitle(id, seed.title); err != nil {
					t.Fatal(err)
				}
			}
		},
	})
	s.WaitFor("Ask k-brain anything")
	s.Type("/dashboard")
	s.Press(tuicap.Enter)
	s.WaitFor("Dashboard")
	s.WaitFor("This workspace")
	s.WaitFor("Parser rewrite here")
	s.WaitFor("Other workspaces")
	s.WaitFor("Gateway rate limiter")
	s.WaitFor("Console styling pass")
	s.WaitFor("3 sessions")

	s.Type("/gate")
	s.WaitFor("1 of 3 sessions")
	s.WaitGone("Console styling pass")
	s.Press(tuicap.Esc)
	s.WaitGone("1 of 3 sessions")
	s.WaitFor("Console styling pass")

	s.Press(tuicap.Tab)
	s.WaitFor("by workspace")
	s.WaitFor("/srv/api-gateway")

	s.Press(tuicap.Esc)
	s.WaitFor("Ask k-brain anything")
}
