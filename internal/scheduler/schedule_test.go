package schedule

import (
	"testing"
	"time"
)

func TestParseEvery(t *testing.T) {
	cases := map[string]time.Duration{
		"@every 10m":   10 * time.Minute,
		"@every 30s":   30 * time.Second,
		"@every 1h":    time.Hour,
		"@every 2d":    48 * time.Hour,
		"  @every 5m ": 5 * time.Minute,
	}
	for expr, want := range cases {
		s, err := Parse(expr)
		if err != nil || s.Every != want {
			t.Fatalf("Parse(%q) = %+v, %v; want every %s", expr, s, err, want)
		}
		if got := s.String(); got != "@every "+want.String() {
			t.Fatalf("String() = %q", got)
		}
	}
}

func TestParseAt(t *testing.T) {
	s, err := Parse("@at 2026-07-26T17:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if s.At.Format(time.RFC3339) != "2026-07-26T17:00:00Z" {
		t.Fatalf("at = %s", s.At)
	}
	for _, bad := range []string{"@at tuesday", "@every", "@every 0m", "@every xm", "*/5 * * * *", ""} {
		if _, err := Parse(bad); err == nil {
			t.Fatalf("Parse(%q) should fail", bad)
		}
	}
}

func TestGridAnchoring(t *testing.T) {
	s, _ := Parse("@every 10m")
	anchor := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	next, ok := s.NextAfter(anchor, anchor.Add(25*time.Minute))
	if !ok || next.Format("15:04") != "12:30" {
		t.Fatalf("grid should not drift: next=%s ok=%v", next, ok)
	}

	next, _ = s.NextAfter(anchor, anchor.Add(30*time.Minute))
	if next.Format("15:04") != "12:40" {
		t.Fatalf("next after 12:30 = %s", next)
	}
}

func TestOneShot(t *testing.T) {
	at := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	s, _ := Parse("@at " + at.Format(time.RFC3339))
	if next, ok := s.NextAfter(time.Now(), time.Now()); !ok || !next.Equal(at) {
		t.Fatalf("pending one-shot: next=%s ok=%v", next, ok)
	}
	if _, ok := s.NextAfter(at, at.Add(time.Minute)); ok {
		t.Fatal("a fired one-shot is done")
	}
}

func TestStringAt(t *testing.T) {
	s, err := Parse("@at 2026-07-26T17:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.String(); got != "@at 2026-07-26T17:00:00Z" {
		t.Fatalf("String() = %q", got)
	}
}
