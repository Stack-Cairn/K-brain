package config

import (
	"os"
	"path/filepath"
	"strings"
)

const MeSeed = `# Your standing instructions for k-brain — appended to every session's
# system prompt, after the built-in operating rules. Lines starting with #
# are comments. Edit freely; /me opens this file.

# Examples:
# - Always run tests with pnpm, never npm.
# - I review every commit message before you commit — always show me the message first.
# - Never touch files under deploy/prod/ without asking.
`

func MePath() string {
	dir, err := Dir()
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, "me.md")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte(MeSeed), 0o600); err != nil {
			return ""
		}
	}
	return path
}

func MeInstructions() string {
	path := MePath()
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var lines []string
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}
