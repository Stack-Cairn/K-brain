package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/skills"
)

func expandSkills(text string, sk []skills.Skill) string {
	var used []string
	for tok := range strings.FieldsSeq(text) {
		if !strings.HasPrefix(tok, "$") || len(tok) < 2 {
			continue
		}
		name := strings.TrimRight(tok[1:], ".,;:!?)\"'")
		for _, s := range sk {
			if s.Name == name {
				used = append(used, s.Name+" ("+s.Path+")")
				break
			}
		}
	}
	if len(used) == 0 {
		return text
	}
	return text + "\n\n[note: the user invoked skill(s): " + strings.Join(used, "; ") +
		" — read each SKILL.md with the read tool and follow its instructions for this request]"
}

var rangeRe = regexp.MustCompile(`#(\d+)(?:-(\d+))?$`)

func mentionPaths(text string) [][2]string {
	var out [][2]string
	fields := strings.Fields(text)
	for i := 0; i < len(fields); i++ {
		tok := fields[i]
		if !strings.HasPrefix(tok, "@") || len(tok) < 2 {
			continue
		}

		for j := i; j < len(fields); j++ {
			cand := strings.Join(fields[i:j+1], " ")
			cand = cand[1:]
			cand = strings.TrimRight(cand, ".,;:!?)\"'")
			lines := ""
			if m := rangeRe.FindStringSubmatch(cand); m != nil {
				cand = strings.TrimSuffix(cand, m[0])
				lines = " (lines " + m[1]
				if m[2] != "" {
					lines += "-" + m[2]
				}
				lines += ")"
			}
			if abs, ok := resolveMentionPath(unescapePath(cand)); ok {
				out = append(out, [2]string{abs, lines})
				i = j
				break
			}
		}
	}
	return out
}

func unescapePath(s string) string {
	return strings.ReplaceAll(s, `\ `, " ")
}

func expandMentions(text string) string {
	mentions := mentionPaths(text)
	notes := make([]string, 0, len(mentions))
	for _, m := range mentions {
		notes = append(notes, m[0]+m[1])
	}
	if len(notes) == 0 {
		return text
	}
	return text + "\n\n[note: the user tagged " + strings.Join(notes, "; ") +
		" — contents are not inlined; inspect with your tools as needed]"
}

var imageExtsForMention = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true,
}

func imageParts(text string) ([]ai.ContentPart, string) {
	var parts []ai.ContentPart
	var names []string
	for _, m := range mentionPaths(text) {
		abs := m[0]
		if !imageExtsForMention[strings.ToLower(filepath.Ext(abs))] {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(abs)), ".")

		ext, data = ai.NormalizeImage(ext, data)
		parts = append(parts, ai.ImagePart(ext, data))
		names = append(names, abs)
	}
	if len(parts) == 0 {
		return nil, ""
	}
	return parts, "\n\n[note: the user attached image(s): " + strings.Join(names, "; ") +
		" — they are inlined above as vision input]"
}
