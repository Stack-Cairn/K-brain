package tui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func setupWizard(cfg *config.Config, r *bufio.Reader) error {

	st, statErr := os.Stdin.Stat()
	if statErr != nil || st.Mode()&os.ModeCharDevice == 0 {
		return nil
	}
	return runSetupWizard(cfg, r, os.Stderr)
}

func runSetupWizard(cfg *config.Config, stdin io.Reader, stderr io.Writer) error {
	r, ok := stdin.(*bufio.Reader)
	if !ok {
		r = bufio.NewReader(stdin)
	}
	w := stderr

	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Welcome to k-brain! First-run setup (Enter = skip/keep default).")
	fmt.Fprintln(w, "Every choice is reversible later: ctrl+p, ~/.k-brain/config.json.")
	fmt.Fprintln(w, "")

	fmt.Fprintln(w, "Configure your API provider baseUrl, apiKey, and model IDs in ~/.k-brain/config.json.")

	if !askYN(r, w, "Show thinking (reasoning) tokens in the transcript?", true) {
		off := false
		cfg.Thinking = &off
	}

	if err := cfg.Save(); err != nil {
		return fmt.Errorf("saving setup choices: %w", err)
	}
	config.MarkSetupDone()
	fmt.Fprintln(w, "Setup complete — starting k-brain.")
	fmt.Fprintln(w, "")
	return nil
}

func askYN(r *bufio.Reader, w io.Writer, question string, def bool) bool {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	for attempt := 0; ; attempt++ {
		fmt.Fprintf(w, "%s %s ", question, hint)
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return def
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "":
			return def
		case "y", "yes":
			return true
		case "n", "no":
			return false
		}
		if attempt > 0 {
			return def
		}
		fmt.Fprintln(w, "  (answer y or n)")
	}
}
