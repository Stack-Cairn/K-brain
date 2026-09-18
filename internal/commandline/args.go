package commandline

import (
	"fmt"
	"strings"
	"unicode"
)

func Split(text string) ([]string, error) {
	var args []string
	var b strings.Builder
	var quote rune
	started := false
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\\' && i+1 < len(runes) && (runes[i+1] == '"' || runes[i+1] == '\'') && quote != '\'' {
			b.WriteRune(runes[i+1])
			i++
			started = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
			started = true
			continue
		}
		if r == '"' || r == '\'' {
			quote = r
			started = true
			continue
		}
		if unicode.IsSpace(r) {
			if started {
				args = append(args, b.String())
				b.Reset()
				started = false
			}
			continue
		}
		b.WriteRune(r)
		started = true
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed quote in arguments")
	}
	if started {
		args = append(args, b.String())
	}
	return args, nil
}
