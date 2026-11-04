package tools

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// editMatch is where edit's old_string landed in a file: the byte spans to replace,
// in order, and the text that replaces each.
type editMatch struct {
	spans    [][2]int
	newText  string
	strategy string
}

// matchEdit finds old in text. An exact match wins; otherwise the LiveAgent matcher
// tolerates line endings, trailing whitespace and indentation, and then look-alike
// characters are folded (smart quotes, dashes, non-breaking spaces). A match found
// more than once is refused unless replaceAll is set.
func matchEdit(text, old, newText string, replaceAll bool) (editMatch, error) {
	if old == "" {
		return editMatch{}, errors.New("old_string is empty; to create a file or replace all of it, use write")
	}
	m := editMatch{newText: newText, strategy: "exact"}
	if strings.Contains(text, old) {
		m.spans = occurrences(text, old)
	} else if actual, repl, strategy, err := liveagentEditMatch(text, old, newText); err == nil {
		m.spans, m.newText, m.strategy = occurrences(text, actual), repl, strategy
	} else if errors.Is(err, errEditAmbiguous) {
		return editMatch{}, errors.New("old_string matches several places once whitespace is ignored, each indented differently; include more surrounding lines or copy the exact text from read")
	} else if spans := lookalikeSpans(text, old); len(spans) > 0 {
		m.spans, m.strategy = spans, "lookalike"
	} else {
		return editMatch{}, errors.New("old_string not found" + nearestLineHint(text, old))
	}
	if len(m.spans) > 1 && !replaceAll {
		return editMatch{}, fmt.Errorf("old_string appears %d times (first at line %d); make it unique or set replace_all", len(m.spans), 1+strings.Count(text[:m.spans[0][0]], "\n"))
	}
	return m, nil
}

func (m editMatch) apply(text string) string {
	var b strings.Builder
	last := 0
	for _, span := range m.spans {
		b.WriteString(text[last:span[0]])
		b.WriteString(m.newText)
		last = span[1]
	}
	b.WriteString(text[last:])
	return b.String()
}

func occurrences(text, sub string) [][2]int {
	var spans [][2]int
	for at := 0; ; {
		i := strings.Index(text[at:], sub)
		if i < 0 {
			return spans
		}
		spans = append(spans, [2]int{at + i, at + i + len(sub)})
		at += i + len(sub)
	}
}

// lookalike maps characters models routinely swap for their ASCII forms.
func lookalike(r rune) rune {
	switch r {
	case '\u2018', '\u2019', '\u201A', '\u201B', '\u2032':
		return '\''
	case '\u201C', '\u201D', '\u201E', '\u201F', '\u2033':
		return '"'
	case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212':
		return '-'
	case '\u00A0', '\u2007', '\u2009', '\u202F':
		return ' '
	}
	return r
}

// foldLookalikes folds s and returns, for each byte of the result plus its end, the
// offset in s of the character that produced it.
func foldLookalikes(s string) (string, []int) {
	var b strings.Builder
	offs := make([]int, 0, len(s)+1)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		before := b.Len()
		if folded := lookalike(r); folded != r {
			b.WriteRune(folded)
		} else {
			// Unchanged bytes are copied as they are, invalid UTF-8 included.
			b.WriteString(s[i : i+size])
		}
		for range b.Len() - before {
			offs = append(offs, i)
		}
		i += size
	}
	return b.String(), append(offs, len(s))
}

// lookalikeSpans finds old in text after folding look-alike characters in both,
// and returns the matches as spans of the original text.
func lookalikeSpans(text, old string) [][2]int {
	foldedText, offs := foldLookalikes(text)
	foldedOld, _ := foldLookalikes(old)
	if foldedText == text && foldedOld == old {
		return nil
	}
	var spans [][2]int
	for _, span := range occurrences(foldedText, foldedOld) {
		spans = append(spans, [2]int{offs[span[0]], offs[span[1]]})
	}
	return spans
}

// nearestLineHint says where old_string most nearly matches, so the model can fix
// its text instead of guessing: the first line where a matching run of lines stops
// matching, or else the file line closest to old_string's first line.
func nearestLineHint(text, old string) string {
	const retry = "; read the file around that line and retry with its exact text"
	fileLines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	oldLines := strings.Split(strings.ReplaceAll(old, "\r\n", "\n"), "\n")
	probe := -1
	for i, line := range oldLines {
		if strings.TrimSpace(line) != "" {
			probe = i
			break
		}
	}
	if probe < 0 || len(fileLines) > 50_000 {
		return ""
	}
	key := func(s string) string { f, _ := foldLookalikes(strings.TrimSpace(s)); return f }
	want := key(oldLines[probe])
	best, bestScore := -1, 0.0
	for i, line := range fileLines {
		got := key(line)
		if got == want {
			// The first line matches: report where the run of lines diverges.
			for k := probe + 1; k < len(oldLines); k++ {
				at := i + k - probe
				if at >= len(fileLines) {
					return fmt.Sprintf("; old_string line %d matches line %d of the file, but old_string runs past the end of the file%s", probe+1, i+1, retry)
				}
				if key(oldLines[k]) != key(fileLines[at]) {
					return fmt.Sprintf("; old_string line %d matches line %d of the file, but old_string line %d differs from line %d:\n  file:       %s\n  old_string: %s\n%s", probe+1, i+1, k+1, at+1, quoteLine(fileLines[at]), quoteLine(oldLines[k]), strings.TrimPrefix(retry, "; "))
				}
			}
			return fmt.Sprintf("; lines %d-%d of the file match old_string apart from whitespace that could not be reconciled%s", i+1, i+len(oldLines)-probe, retry)
		}
		if score := bigramSimilarity(want, got); score > bestScore {
			best, bestScore = i, score
		}
	}
	if best < 0 || bestScore < 0.5 {
		return fmt.Sprintf("; no line in the file resembles old_string's first line %s — check that this is the right file", quoteLine(oldLines[probe]))
	}
	return fmt.Sprintf("; the closest line is line %d:\n  file:       %s\n  old_string: %s\n%s", best+1, quoteLine(fileLines[best]), quoteLine(oldLines[probe]), strings.TrimPrefix(retry, "; "))
}

func quoteLine(line string) string {
	return fmt.Sprintf("%q", clipText(strings.TrimRight(line, "\r"), 200))
}

func clipText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// bigramSimilarity is the Dice coefficient of the strings' character bigrams: 1 for
// equal strings, near 0 for unrelated ones.
func bigramSimilarity(a, b string) float64 {
	if a == b {
		return 1
	}
	ra, rb := []rune(a), []rune(b)
	if len(ra) < 2 || len(rb) < 2 {
		return 0
	}
	if len(ra) > 500 {
		ra = ra[:500]
	}
	if len(rb) > 500 {
		rb = rb[:500]
	}
	grams := make(map[[2]rune]int, len(ra))
	for i := 0; i+1 < len(ra); i++ {
		grams[[2]rune{ra[i], ra[i+1]}]++
	}
	shared := 0
	for i := 0; i+1 < len(rb); i++ {
		g := [2]rune{rb[i], rb[i+1]}
		if grams[g] > 0 {
			grams[g]--
			shared++
		}
	}
	return 2 * float64(shared) / float64(len(ra)-1+len(rb)-1)
}
