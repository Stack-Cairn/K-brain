package bashrun

import (
	"os"
	"runtime"
	"strings"
)

// SecretEnvVars are credentials handed to K-brain processes by their parent. They
// authenticate callers of the backend API (or of the computer helper) and must
// never reach commands the agent runs.
var SecretEnvVars = []string{"K_BRAIN_BACKEND_TOKEN", "K_BRAIN_COMPUTER_TOKEN"}

// UnsetSecretEnv removes SecretEnvVars from this process's environment, so every
// child started afterwards inherits an environment without them. Call it once the
// values have been read.
func UnsetSecretEnv() {
	for _, name := range SecretEnvVars {
		_ = os.Unsetenv(name)
	}
}

// ScrubEnv returns env without SecretEnvVars entries.
func ScrubEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if isSecretEnv(name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func isSecretEnv(name string) bool {
	for _, secret := range SecretEnvVars {
		// Windows environment names are case-insensitive.
		if name == secret || (runtime.GOOS == "windows" && strings.EqualFold(name, secret)) {
			return true
		}
	}
	return false
}
