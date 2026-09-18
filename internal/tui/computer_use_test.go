package tui

import (
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/computer"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestComputerUseCommand(t *testing.T) {
	oldP := tools.ComputerPolicy
	defer func() { tools.ComputerPolicy = oldP }()
	tools.ComputerPolicy = computer.NewPolicy([]string{"Google Chrome"}, nil, true)
	m := &model{}

	m.computerUseCommand(nil, "/computer-use")
	if last := lastBlock(m); !strings.Contains(last, "computer-use") {
		t.Fatalf("status: %q", last)
	}

	if computer.Available() {
		m.computerUseCommand([]string{"allow", "Safari"}, "/computer-use allow Safari")
		if err := tools.ComputerPolicy.Check("Safari"); err != nil {
			t.Fatalf("allow must unblock: %v", err)
		}

		m.computerUseCommand([]string{"deny", "Safari"}, "/computer-use deny Safari")
		if err := tools.ComputerPolicy.Check("Safari"); err == nil {
			t.Fatal("deny must re-block")
		}
	}
}

func TestComputerUseTaskSubmits(t *testing.T) {
	msg := computerUseInstruction("check my calendar")
	if !strings.Contains(msg, "computer_exec") || !strings.Contains(msg, "check my calendar") {
		t.Fatalf("instruction must carry the tool steer + task, got %.100q", msg)
	}
}
