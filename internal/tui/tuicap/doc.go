// Package tuicap drives a real kn binary in a pseudo-terminal for tests. It
// builds ./cmd/kn once per test binary, starts it in a PTY against a fake
// streaming provider with a throwaway data directory, feeds it keystrokes and
// reads the screen back as a text grid through a VT emulator.
//
// It is behind the tuicap build tag, so the default test run never builds or
// starts kn:
//
//	go test -tags tuicap ./internal/tui/tuicap/
//
// A test in another package uses it the same way:
//
//	s := tuicap.Start(t, tuicap.Options{Replies: []tuicap.Reply{tuicap.Text("hi")}})
//	s.WaitFor("Ask k-brain anything")
//	s.Type("hello")
//	s.Press(tuicap.Enter)
//	s.WaitFor("hi")
//	t.Log(s.Snapshot())
//
// Set TUICAP_KN to a prebuilt binary to skip the build.
package tuicap
