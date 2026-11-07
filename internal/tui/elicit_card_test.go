package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/elicit"
)

func ptrInt(n int) *int           { return &n }
func ptrFloat(x float64) *float64 { return &x }
func elicitTestFields() []ai.ElicitField {
	return []ai.ElicitField{
		{Name: "username", Type: "string", Title: "User name", Required: true, MinLength: ptrInt(2)},
		{Name: "password", Type: "string", Title: "Password", Required: true, Sensitive: true},
		{Name: "plan", Type: "string", Title: "Plan", Default: "free", Options: []ai.ElicitOption{{Value: "free", Label: "Free"}, {Value: "pro", Label: "Pro"}}},
		{Name: "seats", Type: "integer", Title: "Seats", Required: true, Minimum: ptrFloat(1), Maximum: ptrFloat(50)},
		{Name: "notify", Type: "boolean", Title: "Email me", Default: true},
		{Name: "topics", Type: "array", Title: "Topics", MaxItems: ptrInt(2), Options: []ai.ElicitOption{{Value: "news", Label: "news"}, {Value: "billing", Label: "billing"}}},
	}
}

func goldenElicitRequest() elicit.Request {
	return elicit.Request{Server: "tracker", Tool: "login", Message: "Sign in to the issue tracker to file the report.", Fields: elicitTestFields()}
}

func openElicit(m *model, req elicit.Request) chan elicit.Response {
	reply := make(chan elicit.Response, 1)
	m.Update(elicitRequest{req: req, reply: reply})
	return reply
}

func typeInto(m *model, s string) {
	for _, r := range s {
		m.elicitKey(keyRunes(string(r)))
	}
}

func elicitText(m *model) string { return ansi.Strip(m.elicitView()) }

func TestElicitCardAcceptsAFilledForm(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(100, 60))
	reply := openElicit(m, goldenElicitRequest())

	v := elicitText(m)
	for _, want := range []string{"tracker asks for input", "MCP server tracker asks while running its tool login", "Sign in to the issue tracker", "looks like a secret (Password)", "trust tracker", "User name *", "Plan: Free", "Email me: [✓] yes", "Accept", "Decline", "Cancel"} {
		if !strings.Contains(v, want) {
			t.Fatalf("card is missing %q:\n%s", want, v)
		}
	}

	typeInto(m, "ada")
	m.elicitKey(ck(tea.KeyEnter))
	typeInto(m, "hunter2")
	if v := elicitText(m); strings.Contains(v, "hunter2") || !strings.Contains(v, "•••••••") {
		t.Fatalf("a secret field must be masked:\n%s", v)
	}
	m.elicitKey(ck(tea.KeyDown))
	m.elicitKey(ck(tea.KeyRight)) // Free -> Pro
	m.elicitKey(ck(tea.KeyDown))
	typeInto(m, "3")
	m.elicitKey(ck(tea.KeyDown))
	m.elicitKey(ck(tea.KeySpace)) // notify off
	m.elicitKey(ck(tea.KeyDown))
	m.elicitKey(ck(tea.KeyRight))
	m.elicitKey(ck(tea.KeySpace)) // billing
	m.elicitKey(ck(tea.KeyLeft))
	m.elicitKey(ck(tea.KeySpace)) // news
	m.elicitKey(ck(tea.KeyEnter)) // to the actions, Accept
	if m.elicitOpen() == false {
		t.Fatal("Enter on the last field moves to the actions, it does not answer")
	}
	m.elicitKey(ck(tea.KeyEnter))
	if m.elicitOpen() {
		t.Fatal("Accept closes the card")
	}
	got := <-reply
	want := map[string]any{"username": "ada", "password": "hunter2", "plan": "pro", "seats": float64(3), "notify": false, "topics": []any{"news", "billing"}}
	if got.Action != elicit.Accept || got.Reason != elicit.ReasonUser || !reflect.DeepEqual(got.Content, want) {
		t.Fatalf("reply = %+v, want accept with %v", got, want)
	}
}

func TestElicitCardShowsWhatIsWrongAndStaysOpen(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(100, 60))
	reply := openElicit(m, goldenElicitRequest())
	typeInto(m, "a") // too short
	m.elicitKey(ck(tea.KeyDown))
	m.elicitKey(ck(tea.KeyDown))
	m.elicitKey(ck(tea.KeyDown))
	typeInto(m, "99")
	m.elicitKey(ck(tea.KeyDown))
	if v := elicitText(m); !strings.Contains(v, "must be at most 50") {
		t.Fatalf("a field the user left shows what is wrong with it:\n%s", v)
	}
	if v := elicitText(m); strings.Contains(v, "is required") {
		t.Fatalf("an untouched required field is not an error before Accept:\n%s", v)
	}
	m.elicitKey(ck(tea.KeyUp))
	m.elicitKey(ck(tea.KeyUp))
	m.elicitKey(ck(tea.KeyUp))
	m.elicitKey(ck(tea.KeyUp))
	m.elicitKey(ck(tea.KeyUp)) // wraps to the actions
	m.elicitKey(ck(tea.KeyEnter))
	if !m.elicitOpen() {
		t.Fatal("an invalid form must not be accepted")
	}
	v := elicitText(m)
	for _, want := range []string{"must be at least 2 characters", "Password *: empty", "is required", "must be at most 50"} {
		if !strings.Contains(v, want) {
			t.Fatalf("after Accept the card shows %q:\n%s", want, v)
		}
	}
	if d := m.cards.elicit; d.focus != 0 {
		t.Fatalf("Accept moves to the first field at fault, focus = %d", d.focus)
	}
	select {
	case r := <-reply:
		t.Fatalf("no reply while the form is invalid, got %+v", r)
	default:
	}

	m.elicitKey(ck(tea.KeyCtrlU))
	m.elicitKey(keyRunes("ada")) // a burst of keys goes into the field
	m.elicitKey(ck(tea.KeyDown))
	typeInto(m, "pw")
	m.elicitKey(ck(tea.KeyDown))
	m.elicitKey(ck(tea.KeyDown))
	m.elicitKey(ck(tea.KeyBackspace))
	m.elicitKey(ck(tea.KeyBackspace))
	typeInto(m, "x")
	if v := elicitText(m); !strings.Contains(v, "must be a whole number") {
		t.Fatalf("a number field rejects text:\n%s", v)
	}
	m.elicitKey(ck(tea.KeyBackspace))
	typeInto(m, "2")
	for m.cards.elicit.focus != len(m.cards.elicit.inputs) {
		m.elicitKey(ck(tea.KeyTab))
	}
	m.elicitKey(ck(tea.KeyEnter))
	r := <-reply
	if r.Action != elicit.Accept || r.Content["username"] != "ada" || r.Content["seats"] != float64(2) || r.Content["plan"] != "free" {
		t.Fatalf("reply = %+v", r)
	}
	if _, ok := r.Content["topics"]; ok {
		t.Fatalf("an optional multiple choice with nothing picked is left out: %v", r.Content)
	}
}

func TestElicitCardDeclineCancelAndClose(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []tea.KeyMsg
		want string
	}{
		{"decline", []tea.KeyMsg{ck(tea.KeyShiftTab), ck(tea.KeyRight), ck(tea.KeyEnter)}, elicit.Decline},
		{"cancel row", []tea.KeyMsg{ck(tea.KeyUp), ck(tea.KeyLeft), ck(tea.KeyEnter)}, elicit.Cancel},
		{"esc", []tea.KeyMsg{ck(tea.KeyEsc)}, elicit.Cancel},
		{"ctrl+c", []tea.KeyMsg{ck(tea.KeyCtrlC)}, elicit.Cancel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := compactCmdModel()
			m.Update(mkWinSize(100, 60))
			reply := openElicit(m, goldenElicitRequest())
			for _, k := range tc.keys {
				m.Update(k)
			}
			if m.elicitOpen() {
				t.Fatal("card still open")
			}
			if r := <-reply; r.Action != tc.want || r.Content != nil || r.Reason != elicit.ReasonUser {
				t.Fatalf("reply = %+v, want %s", r, tc.want)
			}
		})
	}

	m := compactCmdModel()
	reply := openElicit(m, goldenElicitRequest())
	m.Update(elicitClose{reply: make(chan elicit.Response)})
	if !m.elicitOpen() {
		t.Fatal("a close for another request leaves the card open")
	}
	m.Update(elicitClose{reply: reply})
	if m.elicitOpen() {
		t.Fatal("a close for this request removes the card")
	}
}

func TestElicitCardChoices(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(100, 60))
	opts := []ai.ElicitOption{{Value: "a", Label: "A"}, {Value: "b", Label: "B"}}
	reply := openElicit(m, elicit.Request{Server: "s", Message: "pick", Fields: []ai.ElicitField{
		{Name: "must", Type: "string", Required: true, Options: opts},
		{Name: "may", Type: "string", Options: opts, Default: "b"},
	}})
	if v := elicitText(m); !strings.Contains(v, "must *: < not chosen >") {
		t.Fatalf("a required choice without a default starts empty:\n%s", v)
	}
	m.elicitKey(ck(tea.KeyLeft))
	if v := elicitText(m); !strings.Contains(v, "< B > 2/2") {
		t.Fatalf("Left from none goes to the last option:\n%s", v)
	}
	m.elicitKey(ck(tea.KeyRight))
	m.elicitKey(ck(tea.KeyRight))
	if d := m.cards.elicit; d.inputs[0].choice != 1 {
		t.Fatalf("a required choice never goes back to none: %d", d.inputs[0].choice)
	}
	m.elicitKey(ck(tea.KeyDown))
	m.elicitKey(ck(tea.KeyRight)) // b -> none
	if d := m.cards.elicit; d.inputs[1].choice != -1 {
		t.Fatalf("an optional choice can be emptied: %d", d.inputs[1].choice)
	}
	m.elicitKey(ck(tea.KeyDown))
	m.elicitKey(ck(tea.KeyEnter))
	if r := <-reply; !reflect.DeepEqual(r.Content, map[string]any{"must": "b"}) {
		t.Fatalf("reply = %+v", r)
	}
}

// Text an MCP server sends is shown as plain text: it cannot recolour the
// card, move the cursor or reverse the line to spoof the server name.
func TestElicitCardShowsServerTextPlain(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(100, 60))
	openElicit(m, elicit.Request{Server: "docs", Message: "\x1b[2J\x1b[31mred\x1b[0m\u202edone\x07", Fields: []ai.ElicitField{
		{Name: "x", Type: "string", Title: "Ti\x1b]8;;http://e\x07tle", Description: "line\x1b[1A up"},
	}})
	raw := m.elicitView()
	for _, bad := range []string{"\x1b[2J", "\x1b[31m", "\x1b[1A", "\x1b]8", "\u202e", "\x07"} {
		if strings.Contains(raw, bad) {
			t.Fatalf("card carries %q from the server", bad)
		}
	}
	if v := ansi.Strip(raw); !strings.Contains(v, "reddone") || !strings.Contains(v, "Title") || !strings.Contains(v, "line up") {
		t.Fatalf("plain text kept:\n%s", v)
	}
}

// A form taller than the screen scrolls its fields and keeps the focused one
// and the actions in view.
func TestElicitCardScrollsToTheFocusedField(t *testing.T) {
	m := fullModel()
	var fields []ai.ElicitField
	for _, n := range []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliett", "kilo", "lima", "mike", "november"} {
		fields = append(fields, ai.ElicitField{Name: n, Type: "string", Description: "the " + n + " value"})
	}
	openElicit(m, elicit.Request{Server: "many", Message: "Fill in everything.", Fields: fields})
	m.layout()
	for range 12 {
		m.elicitKey(ck(tea.KeyDown))
	}
	view := m.View()
	if got := lipgloss.Height(view); got > m.height {
		t.Fatalf("frame is %d rows on a %d-row terminal", got, m.height)
	}
	plain := ansi.Strip(view)
	for _, want := range []string{"many asks for input", "mike", "Accept"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("%q not on screen:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "alpha:") {
		t.Fatalf("the first field should have scrolled away:\n%s", plain)
	}
}

func TestElicitHookWithoutProgramDeclines(t *testing.T) {
	m := compactCmdModel()
	prev := elicit.Default
	defer func() { elicit.Default = prev }()
	m.installElicitHook()
	r := elicit.Default(context.Background(), goldenElicitRequest())
	if r.Action != elicit.Decline || r.Reason != elicit.ReasonNoClient {
		t.Fatalf("no program: %+v", r)
	}
}

func TestElicitCardCountsAsAWaitingCard(t *testing.T) {
	m := fullModel()
	m.busy = true
	openElicit(m, goldenElicitRequest())
	m.layout()
	if !strings.Contains(ansi.Strip(m.busyLeft()), "Waiting for your answer") {
		t.Fatalf("busy row: %q", ansi.Strip(m.busyLeft()))
	}
	if !strings.HasPrefix(m.windowTitle(m.nowFn()), glyphs.ToolQueued) {
		t.Fatalf("window title: %q", m.windowTitle(m.nowFn()))
	}
	sum := 0
	for _, s := range m.chromeSections() {
		sum += s.rows(m)
	}
	if m.fixedChrome() != sum || m.elicitRows() == 0 {
		t.Fatalf("chrome sections do not count the form: fixed %d, sum %d", m.fixedChrome(), sum)
	}
}
