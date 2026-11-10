package goalloop

import (
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestContinuePromptCarriesGoalAndToken(t *testing.T) {
	p := ContinuePrompt("ship the feature")
	if !strings.Contains(p, "ship the feature") || strings.Count(p, MetToken) != 3 {
		t.Fatalf("prompt: %q", p)
	}
}

func TestMet(t *testing.T) {
	for _, s := range []string{"GOAL_MET — everything verified", "\n  GOAL_MET done", "**GOAL_MET** — shipped", "## GOAL_MET", "> `GOAL_MET`: verified", "GOAL_MET"} {
		if !Met(s) {
			t.Fatalf("%q must count as met", s)
		}
	}
	for _, s := range []string{
		"I am making progress toward GOAL_MET soon",
		"GOAL_METRIC improved",
		"Done. GOAL_MET",
		"Verified: all green.\n\nGOAL_MET — shipped",
		"Not GOAL_MET yet — tests still fail.",
		"I cannot say GOAL_MET: the build is red.",
		"This is not GOAL_MET.",
		"未达成 GOAL_MET，继续工作",
		"GOAL_MET-pending",
		"GOAL_MET_DONE",
		"goal_met done",
		"almost done",
		"",
		strings.Repeat("x", 250) + " GOAL_MET",
	} {
		if Met(s) {
			t.Fatalf("%q must not count as met", s)
		}
	}
}

func TestNeedsInput(t *testing.T) {
	for _, s := range []string{"GOAL_NEEDS_INPUT — which title should the file use?", "**GOAL_NEEDS_INPUT** 文件标题用什么？", "\n> GOAL_NEEDS_INPUT"} {
		if !NeedsInput(s) {
			t.Fatalf("%q must ask for input", s)
		}
		if Met(s) {
			t.Fatalf("%q must not count as met", s)
		}
	}
	for _, s := range []string{
		"文件标题用什么？正文写几句话？",
		"Done. GOAL_NEEDS_INPUT",
		"I will use GOAL_NEEDS_INPUT only if blocked.",
		"GOAL_NEEDS_INPUTS later",
		"GOAL_MET — shipped",
		"",
	} {
		if NeedsInput(s) {
			t.Fatalf("%q must not ask for input", s)
		}
	}
}

func TestRoundPromptsCarryBothTokens(t *testing.T) {
	for name, p := range map[string]string{
		"start":    StartPrompt("ship the feature"),
		"continue": ContinuePrompt("ship the feature"),
		"reply":    ReplyPrompt("ship the feature"),
	} {
		if !strings.Contains(p, "ship the feature") || !strings.Contains(p, MetToken) || !strings.Contains(p, NeedsInputToken) {
			t.Fatalf("%s prompt: %q", name, p)
		}
	}
	if !strings.Contains(StartPrompt("x"), "Work toward it now") {
		t.Fatal("start prompt must keep its instruction")
	}
}

func TestMaxRoundsResolution(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := t.TempDir()
	if n := MaxRounds(dir, 0); n != config.DefaultGoalMaxRounds {
		t.Fatalf("default: %d", n)
	}
	if n := MaxRounds(dir, 7); n != 7 {
		t.Fatalf("global: %d", n)
	}
	if err := config.SetProjectGoalMaxRounds(dir, 3); err != nil {
		t.Fatal(err)
	}
	if n := MaxRounds(dir, 7); n != 3 {
		t.Fatalf("project override: %d", n)
	}
	if n := MaxRounds("", 7); n != 7 {
		t.Fatalf("empty dir skips project lookup: %d", n)
	}
}

func TestContinueGoalRoundTrips(t *testing.T) {
	goal := "Ship the panel\nwith two lines"
	if got, ok := ContinueGoal(ContinuePrompt("Ship the panel")); !ok || got != "Ship the panel" {
		t.Fatalf("ContinueGoal = %q, %v", got, ok)
	}
	if got, ok := ContinueGoal(ContinuePrompt(goal)); !ok || got != goal {
		t.Fatalf("multi-line goal = %q, %v", got, ok)
	}
	for _, other := range []string{"", "hello", StartPrompt("x"), ReplyPrompt("x")} {
		if _, ok := ContinueGoal(other); ok {
			t.Fatalf("ContinueGoal(%q) matched", other)
		}
	}
}
