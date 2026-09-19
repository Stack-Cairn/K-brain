package tui

import (
	"strings"
	"unicode"

	"github.com/mattn/go-runewidth"
)

type ancientCell struct {
	text  string
	width int
}

func renderAncientText(text string, width int) string {
	if width < 8 {
		width = 8
	}
	cells := ancientCells(strings.TrimSpace(strings.ReplaceAll(text, "\n", " ")))
	if len(cells) == 0 {
		return ""
	}
	rows := 8
	if len(cells) < rows {
		rows = len(cells)
	}
	maxCols := max(width/2, 1)
	var out strings.Builder
	for start := 0; start < len(cells); start += maxCols * rows {
		end := min(start+maxCols*rows, len(cells))
		chunk := cells[start:end]
		usedCols := (len(chunk) + rows - 1) / rows
		colWidths := make([]int, usedCols)
		for i, cell := range chunk {
			col := i / rows
			if cell.width > colWidths[col] {
				colWidths[col] = cell.width
			}
		}
		for i := range colWidths {
			if colWidths[i] < 2 {
				colWidths[i] = 2
			}
		}
		total := 0
		for _, n := range colWidths {
			total += n
		}
		if total > width {
			width = total
		}
		for row := 0; row < rows; row++ {
			line := strings.Repeat(" ", max(width-total, 0))
			for col := usedCols - 1; col >= 0; col-- {
				idx := col*rows + row
				cell := ancientCell{}
				if idx < len(chunk) {
					cell = chunk[idx]
				}
				line += cell.text + strings.Repeat(" ", max(colWidths[col]-cell.width, 0))
			}
			out.WriteString(strings.TrimRight(line, " "))
			if row < rows-1 {
				out.WriteByte('\n')
			}
		}
		if end < len(cells) {
			out.WriteString("\n\n")
		}
	}
	return out.String()
}

func ancientCells(text string) []ancientCell {
	var cells []ancientCell
	var word []rune
	flush := func() {
		if len(word) == 0 {
			return
		}
		s := string(word)
		cells = append(cells, ancientCell{text: s, width: runewidth.StringWidth(s)})
		word = nil
	}
	for _, r := range []rune(text) {
		switch {
		case unicode.IsSpace(r):
			flush()
		case ancientWideRune(r):
			flush()
			cells = append(cells, ancientCell{text: string(r), width: max(runewidth.RuneWidth(r), 2)})
		default:
			word = append(word, r)
		}
	}
	flush()
	return cells
}

func ancientWideRune(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r)
}
