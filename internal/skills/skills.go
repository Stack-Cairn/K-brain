package skills

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/datapath"
)

type Skill struct {
	Name        string
	Description string
	Path        string

	DisableModelInvocation bool

	Warning string

	// Root is the scanned skills directory the skill was found in.
	Root string
	// BaseDir is the skill's directory relative to Root, with forward slashes
	// on every platform: "radare2" for a top-level skill,
	// "reverse-skill/skills/radare2" for one inside a skill pack.
	BaseDir string
	// Pack is the top-level directory of the skill pack that holds the skill;
	// empty for a top-level skill.
	Pack string
	// ShadowedBy is the BaseDir of the skill that kept this skill's name. It is
	// set only on shadowed copies, which ScanAll reports apart from the skills.
	ShadowedBy string
}

type ScanProblem struct {
	Path string
	Err  string
}

func DefaultDirs() []string {
	wd, _ := os.Getwd()
	return DirsFor(wd)
}

func DirsFor(project string) []string {
	var dirs []string
	if project != "" {
		datapath.Report(datapath.MigrateProjectDir(project))
		dirs = append(dirs, filepath.Join(datapath.ProjectDir(project), "skills"), filepath.Join(project, ".agents", "skills"))
		// Claude Code project skills; later directories lose name collisions.
		if config.RulesCompatSettings().ClaudeEnabled() {
			dirs = append(dirs, filepath.Join(project, ".claude", "skills"))
		}
	}
	if home, err := datapath.UserDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "skills"))
		if userHome, err := os.UserHomeDir(); err == nil {
			dirs = append(dirs, filepath.Join(userHome, ".agents", "skills"))
		}
	} else {
		datapath.Report(err)
	}
	return dirs
}

func ForeignDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".codex", "skills"),
		filepath.Join(home, ".claude", "skills"),
	}
}

func Scan(dirs ...string) []Skill {
	sk, _ := ScanDetailed(dirs...)
	return sk
}

// ScanDetailed returns the skills found in dirs and the SKILL.md files that
// could not be used. Shadowed skill-pack copies are left out; see ScanAll.
func ScanDetailed(dirs ...string) ([]Skill, []ScanProblem) {
	r := ScanAll(dirs...)
	return r.Skills, r.Problems
}

func parse(path string) (Skill, error) {
	f, err := os.Open(path)
	if err != nil {
		return Skill{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return Skill{}, fmt.Errorf("%s: no frontmatter", path)
	}
	s := Skill{Path: path}
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			break
		}
		key, v, ok := cutKey(line)
		if !ok {
			continue
		}
		if isBlockScalarIndicator(v) {

			v = readBlockScalar(sc, v, indentOf(line))
		} else {
			v = unquote(v)
		}
		switch key {
		case "name":
			s.Name = v
		case "description":
			s.Description = v
		case "disable-model-invocation":
			s.DisableModelInvocation = strings.TrimSpace(v) == "true"
		}
	}
	return s, sc.Err()
}

func cutKey(line string) (key, value string, ok bool) {
	if line == "" || line[0] == ' ' || line[0] == '\t' {
		return "", "", false
	}
	k, v, found := strings.Cut(line, ":")
	if !found {
		return "", "", false
	}
	return k, v, true
}

func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

func isBlockScalarIndicator(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || (v[0] != '>' && v[0] != '|') {
		return false
	}
	for _, r := range v[1:] {
		if r != '-' && r != '+' && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func readBlockScalar(sc *bufio.Scanner, header string, keyIndent int) string {
	indicator := strings.TrimSpace(header)
	literal := indicator[0] == '|'
	chomp := byte(0)
	for _, r := range indicator[1:] {
		if r == '-' || r == '+' {
			chomp = byte(r)
		}
	}

	var lines []string

	bodyIndent := -1
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {

			break
		}
		ind := indentOf(line)
		if trimmed != "" {
			if ind <= keyIndent {
				break
			}
			if bodyIndent == -1 {
				bodyIndent = ind
			}
		}
		lines = append(lines, line)
	}

	_ = keyIndent

	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			lines[i] = ""
			continue
		}
		if bodyIndent > 0 && len(line) >= bodyIndent {
			lines[i] = line[bodyIndent:]
		}
	}

	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}

	trailing := 0
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
		trailing++
	}

	var v string
	if literal {
		v = strings.Join(lines, "\n")
	} else {
		v = strings.Join(lines, " ")
	}
	if chomp == '+' && len(lines) > 0 {
		v += "\n" + strings.Repeat("\n", trailing)
	}
	return v
}

func unquote(v string) string {
	v = strings.TrimSpace(v)
	for _, q := range []string{`"`, `'`} {
		if strings.HasPrefix(v, q) && strings.HasSuffix(v, q) && len(v) >= 2 {
			return v[1 : len(v)-1]
		}
	}
	return v
}

const (
	specMaxName = 64
	specMaxDesc = 1024
)

var specNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

func ValidName(name string) bool {
	return specNameRe.MatchString(name)
}

func validate(s Skill) string {
	var problems []string
	if len(s.Name) > specMaxName {
		problems = append(problems, fmt.Sprintf("name exceeds %d characters (%d)", specMaxName, len(s.Name)))
	}
	if !specNameRe.MatchString(s.Name) {
		problems = append(problems, "name must be lowercase a-z, 0-9, hyphens only")
	}
	if strings.HasPrefix(s.Name, "-") || strings.HasSuffix(s.Name, "-") {
		problems = append(problems, "name must not start or end with a hyphen")
	}
	if strings.Contains(s.Name, "--") {
		problems = append(problems, "name must not contain consecutive hyphens")
	}
	if len(s.Description) > specMaxDesc {
		problems = append(problems, fmt.Sprintf("description exceeds %d characters (%d)", specMaxDesc, len(s.Description)))
	}
	return strings.Join(problems, "; ")
}

// PromptPackBudget bounds, in bytes, how much of the <available_skills> block
// one skill pack may take. A pack over budget lists its router (its shallowest
// skill) in full and the rest compactly; see writePack.
const PromptPackBudget = 12 << 10

// promptCompactDesc is how many runes of description a compact pack entry keeps.
const promptCompactDesc = 80

func PromptBlock(sk []Skill) string {
	var visible []Skill
	for _, s := range sk {
		if !s.DisableModelInvocation {
			visible = append(visible, s)
		}
	}
	if len(visible) == 0 {
		return ""
	}
	// Pack skills are grouped under their pack at the position of the pack's
	// first skill; top-level skills keep their place.
	type group struct {
		skill Skill
		pack  []Skill
	}
	var groups []*group
	packs := map[string]*group{}
	for _, s := range visible {
		if s.Pack == "" {
			groups = append(groups, &group{skill: s})
			continue
		}
		key := s.Root + "\x00" + s.Pack
		g := packs[key]
		if g == nil {
			g = &group{}
			packs[key] = g
			groups = append(groups, g)
		}
		g.pack = append(g.pack, s)
	}
	var b strings.Builder
	b.WriteString("\n\n<available_skills>\nThese skills hold task-specific instructions. When one is relevant, read its SKILL.md with the read tool and follow it. Relative paths in a skill resolve against the skill's directory (the parent of its SKILL.md).\n")
	if len(packs) > 0 {
		b.WriteString("Skills inside a <skill_pack> come from one installed pack. A compact entry gives the skill's directory relative to the pack location; its SKILL.md is in that directory.\n")
	}
	for _, g := range groups {
		if g.pack == nil {
			writePromptSkill(&b, g.skill, "  ")
			continue
		}
		writePack(&b, g.pack)
	}
	b.WriteString("</available_skills>")
	return b.String()
}

func writePromptSkill(b *strings.Builder, s Skill, indent string) {
	b.WriteString(indent + "<skill>\n")
	fmt.Fprintf(b, "%s  <name>%s</name>\n", indent, xmlEscape(s.Name))
	fmt.Fprintf(b, "%s  <description>%s</description>\n", indent, xmlEscape(s.Description))
	fmt.Fprintf(b, "%s  <location>%s</location>\n", indent, xmlEscape(s.Path))
	b.WriteString(indent + "</skill>\n")
}

// writePack lists one pack. A pack that fits PromptPackBudget lists every
// skill like a top-level one. Otherwise its router (the shallowest skill) is
// listed in full and the rest compactly, name, directory and the first
// promptCompactDesc runes of the description, the router's own descendants
// first; entries that no longer fit are counted in a closing <more> line.
func writePack(b *strings.Builder, sk []Skill) {
	first := sk[0]
	packDir := filepath.Join(first.Root, filepath.FromSlash(first.Pack))
	open := fmt.Sprintf("  <skill_pack name=\"%s\" location=\"%s\">\n", xmlEscape(first.Pack), xmlEscape(packDir))
	const closeTag = "  </skill_pack>\n"

	var full strings.Builder
	for _, s := range sk {
		writePromptSkill(&full, s, "    ")
	}
	if len(open)+full.Len()+len(closeTag) <= PromptPackBudget {
		b.WriteString(open)
		b.WriteString(full.String())
		b.WriteString(closeTag)
		return
	}

	router := sk[0]
	for _, s := range sk[1:] {
		if packDepth(s) < packDepth(router) || (packDepth(s) == packDepth(router) && s.BaseDir < router.BaseDir) {
			router = s
		}
	}
	under := func(s Skill) bool { return strings.HasPrefix(s.BaseDir, router.BaseDir+"/") }
	rest := make([]Skill, 0, len(sk)-1)
	for _, s := range sk {
		if s.BaseDir != router.BaseDir {
			rest = append(rest, s)
		}
	}
	sort.SliceStable(rest, func(i, j int) bool {
		if ui, uj := under(rest[i]), under(rest[j]); ui != uj {
			return ui
		}
		if di, dj := packDepth(rest[i]), packDepth(rest[j]); di != dj {
			return di < dj
		}
		return rest[i].BaseDir < rest[j].BaseDir
	})

	var body strings.Builder
	writePromptSkill(&body, Skill{Name: router.Name, Description: TruncateDescription(router.Description, specMaxDesc), Path: router.Path}, "    ")
	// Reserve room for the closing <more> line so the pack never exceeds its budget.
	const moreReserve = 200
	used := len(open) + body.Len() + len(closeTag) + moreReserve
	omitted := 0
	for _, s := range rest {
		dir := strings.TrimPrefix(s.BaseDir, s.Pack+"/")
		line := fmt.Sprintf("    <skill name=\"%s\" dir=\"%s\">%s</skill>\n", xmlEscape(s.Name), xmlEscape(dir), xmlEscape(TruncateDescription(s.Description, promptCompactDesc)))
		if omitted > 0 || used+len(line) > PromptPackBudget {
			omitted++
			continue
		}
		used += len(line)
		body.WriteString(line)
	}
	if omitted > 0 {
		fmt.Fprintf(&body, "    <more count=\"%d\">%d more skills of this pack are not listed; find their SKILL.md files under the pack location.</more>\n", omitted, omitted)
	}
	b.WriteString(open)
	b.WriteString(body.String())
	b.WriteString(closeTag)
}

func xmlEscape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	).Replace(s)
}
