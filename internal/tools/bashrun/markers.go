package bashrun

import (
	"os"
	"strconv"
)

var ChildMarkers = []string{"K_BRAIN=1", "K_BRAIN=1"}

func SetMarkers(sessionID, model string) {
	pid := strconv.Itoa(os.Getpid())
	ChildMarkers = []string{
		"K_BRAIN=1", "K_BRAIN_SESSION_ID=" + sessionID, "K_BRAIN_MODEL=" + model, "K_BRAIN_PID=" + pid,
		"K_BRAIN=1", "K_BRAIN_SESSION_ID=" + sessionID, "K_BRAIN_MODEL=" + model, "K_BRAIN_PID=" + pid,
	}
}
