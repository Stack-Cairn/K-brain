package bashrun

import (
	"context"
	"os/exec"
)

func runInteractive(_ context.Context, _ *exec.Cmd, _ Options) Result {
	return Result{Exit: "interactive PTY is not available on native Windows; run non-interactively or run k-brain inside WSL"}
}
