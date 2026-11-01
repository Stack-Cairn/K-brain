package permissions

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// KB-3 review, round 3. The /proc check looks for the letters "proc" in a word; a
// glob in place of one of them, or a symlink into /proc, still reaches
// /proc/self/environ.
func TestReviewKB3R3ProcEnvironWithoutTheWordProc(t *testing.T) {
	commands := []string{
		"cat /pr?c/self/environ",
		"cd /pr*c/self && cat environ",
		"cd / && cd pro? && cat self/environ",
		"cat /dev/fd/../environ", // /dev/fd -> /proc/self/fd
	}
	if bash, err := exec.LookPath("bash"); err == nil && exec.Command(bash, "-c", "test -r /proc/self/environ").Run() == nil {
		const secret = "kb3-review-r3-s3cr3t"
		for _, command := range commands {
			cmd := exec.Command(bash, "-c", command)
			cmd.Env = append(os.Environ(), "K_BRAIN_REVIEW_SECRET="+secret)
			out, _ := cmd.CombinedOutput()
			if !strings.Contains(string(out), secret) {
				t.Fatalf("expected %q to print the environment, got %.200q", command, out)
			}
		}
	} else {
		t.Log("bash with a readable /proc/self/environ not found; behavior proof skipped")
	}
	for _, command := range commands {
		d := Evaluate(Request{Tool: "Bash", Command: command, Shell: "bash"}, Options{AllowReadOnly: true})
		if d.Effect == Allow && d.Source == SourceReadOnly {
			t.Errorf("Evaluate(%q) = read-only allow; it prints the process environment", command)
		}
	}
}
