package tui

import (
	"strconv"
	"strings"
)

func (m *model) openChoicePrompt(title string, options []string, onOK func(string)) {
	var sb strings.Builder
	sb.WriteString(title + "\n")
	for i, o := range options {
		sb.WriteString(dimStyle.Render("  "+strconv.Itoa(i+1)+") "+o) + "\n")
	}
	sb.WriteString(dimStyle.Render("  type a number (enter for 1)"))
	m.append(sb.String())
	m.openNamePrompt(title+" [1]:", "", func(value string) {
		choice := resolveChoice(strings.TrimSpace(value), options)
		if choice == "" {
			return
		}
		onOK(choice)
	})
	m.namePrompt.mask = false
}

func resolveChoice(value string, options []string) string {
	if len(options) == 0 {
		return ""
	}
	if value == "" {
		return options[0]
	}
	if n, err := strconv.Atoi(value); err == nil && n >= 1 && n <= len(options) {
		return options[n-1]
	}
	for _, o := range options {
		if o == value {
			return o
		}
	}
	return ""
}
