package tui

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func checkTrust(r *bufio.Reader) (bool, error) {
	wd, err := os.Getwd()
	if err != nil {
		return false, err
	}
	if config.Trusted(wd) {
		return true, nil
	}
	st, err := os.Stdin.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice == 0 {

		return false, fmt.Errorf("folder %s is not trusted (run interactively once to trust it, or add it to ~/.k-brain/trusted.json)", wd)
	}
	fmt.Fprintf(os.Stderr, "\nDo you trust the files in this folder?\n%s\n\n", wd)
	fmt.Fprintln(os.Stderr, "k-brain may read files in this folder. Reading untrusted files may lead k-brain to behave in unexpected ways.")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "With your permission k-brain may execute files in this folder. Executing untrusted code is unsafe.")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprint(os.Stderr, "Proceed? [Y/n] ")
	ans, err := r.ReadString('\n')
	if err != nil {
		return false, err
	}
	if a := strings.ToLower(strings.TrimSpace(ans)); a == "" || a == "y" || a == "yes" {
		if err := config.Trust(wd); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}
