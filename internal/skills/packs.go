package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// Skill packs. A top-level directory of a skills root that has no SKILL.md of
// its own but holds SKILL.md files further down (a cloned repository of skills,
// for example) is a skill pack. The LiveAgent desktop applies the same rule, so
// the limits and orderings below are part of the contract:
//
//   - The pack is walked breadth-first, at most PackMaxDepth directory levels
//     below the pack directory, visiting at most PackMaxDirs directories and
//     accepting at most PackMaxSkills skills.
//   - Dot-directories, the names in packSkipDirs and symlinks that resolve
//     outside the skills root are not entered. Skill directories are entered
//     too: a router skill may hold child skills.
//   - Every directory with a SKILL.md whose frontmatter has a name and a
//     description is a skill named by that frontmatter name (lenient checks:
//     the name need not match the directory, see lenientName).
//   - Within a pack the shallowest skill keeps a name, ties going to the
//     byte-wise smaller BaseDir; the others are shadowed. Across the root a
//     top-level skill beats a pack skill, and between packs the pack whose
//     directory name sorts first wins.
const (
	PackMaxDepth  = 4
	PackMaxDirs   = 5000
	PackMaxSkills = 200
)

var packSkipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	"build":        true,
	"dist":         true,
	"__pycache__":  true,
}

// ScanResult is everything a scan of skill directories found.
type ScanResult struct {
	// Skills are the usable skills: top-level skills and the pack skills that
	// kept their names, in directory order with each pack's skills shallowest
	// first.
	Skills []Skill
	// Shadowed are pack skills that lost their name to another skill of the
	// same root; ShadowedBy names the winner.
	Shadowed []Skill
	// Problems are SKILL.md files that could not be used and packs that hit a
	// walk limit.
	Problems []ScanProblem
}

// ScanAll scans each directory for top-level skills and skill packs.
// Directories are independent: names are only de-duplicated within one.
func ScanAll(dirs ...string) ScanResult {
	var r ScanResult
	for _, d := range dirs {
		one := scanRoot(d)
		r.Skills = append(r.Skills, one.Skills...)
		r.Shadowed = append(r.Shadowed, one.Shadowed...)
		r.Problems = append(r.Problems, one.Problems...)
	}
	return r
}

func scanRoot(root string) ScanResult {
	var r ScanResult
	entries, err := os.ReadDir(root)
	if err != nil {
		return r
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}
	// Skills are emitted in directory order; each entry fills its own slot.
	slots := make([][]Skill, len(entries))
	taken := map[string]string{}
	var packs []int
	for i, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(root, e.Name(), "SKILL.md")
		if _, err := os.Stat(p); err != nil {
			if errors.Is(err, fs.ErrNotExist) && packCandidate(e.Name()) {
				packs = append(packs, i)
			}
			continue
		}
		s, err := parse(p)
		if err != nil {
			r.Problems = append(r.Problems, ScanProblem{Path: p, Err: err.Error()})
			continue
		}
		if s.Name == "" {
			s.Name = e.Name()
		}
		if w := validate(s); w != "" {
			s.Warning = w
		}
		s.Root, s.BaseDir = root, e.Name()
		slots[i] = []Skill{s}
		if _, ok := taken[s.Name]; !ok {
			taken[s.Name] = s.BaseDir
		}
	}
	// Packs go after every top-level skill has claimed its name, so a
	// top-level skill wins whatever its directory sorts as.
	for _, i := range packs {
		found, problems := walkPack(root, realRoot, entries[i].Name())
		r.Problems = append(r.Problems, problems...)
		for _, s := range found {
			if s.ShadowedBy != "" {
				r.Shadowed = append(r.Shadowed, s)
				continue
			}
			if by, ok := taken[s.Name]; ok {
				s.ShadowedBy = by
				r.Shadowed = append(r.Shadowed, s)
				continue
			}
			taken[s.Name] = s.BaseDir
			slots[i] = append(slots[i], s)
		}
	}
	for _, s := range slots {
		r.Skills = append(r.Skills, s...)
	}
	return r
}

// packCandidate reports whether a top-level directory may be walked as a pack.
func packCandidate(name string) bool {
	return !strings.HasPrefix(name, ".") && !packSkipDirs[name]
}

type packNode struct {
	rel   string // slash-separated, relative to the skills root
	real  string // resolved path, for loop detection
	depth int    // levels below the pack directory
}

// walkPack walks one pack and returns its skills ordered shallowest first,
// then by BaseDir, followed by the copies that lost their name inside the
// pack (ShadowedBy set).
func walkPack(root, realRoot, pack string) ([]Skill, []ScanProblem) {
	var found []Skill
	var problems []ScanProblem
	packDir := filepath.Join(root, pack)
	queue := []packNode{{rel: pack, real: filepath.Join(realRoot, pack)}}
	seen := map[string]bool{}
	visited := 0
walk:
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if seen[n.real] {
			continue
		}
		seen[n.real] = true
		if visited == PackMaxDirs {
			problems = append(problems, ScanProblem{Path: packDir, Err: fmt.Sprintf("skill pack walk stopped after %d directories", PackMaxDirs)})
			break
		}
		visited++
		abs := filepath.Join(root, filepath.FromSlash(n.rel))
		if n.depth > 0 {
			p := filepath.Join(abs, "SKILL.md")
			if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
				s, err := parse(p)
				if err == nil {
					err = lenientCheck(s)
				}
				switch {
				case err != nil:
					problems = append(problems, ScanProblem{Path: p, Err: err.Error()})
				case len(found) == PackMaxSkills:
					problems = append(problems, ScanProblem{Path: packDir, Err: fmt.Sprintf("skill pack has more than %d skills; the rest are ignored", PackMaxSkills)})
					break walk
				default:
					s.Root, s.BaseDir, s.Pack = root, n.rel, pack
					found = append(found, s)
				}
			}
		}
		if n.depth == PackMaxDepth {
			continue
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") || packSkipDirs[name] {
				continue
			}
			child := packNode{rel: n.rel + "/" + name, real: filepath.Join(n.real, name), depth: n.depth + 1}
			if e.Type()&fs.ModeSymlink != 0 {
				target, err := filepath.EvalSymlinks(filepath.Join(abs, name))
				if err != nil || !within(realRoot, target) {
					continue
				}
				if info, err := os.Stat(target); err != nil || !info.IsDir() {
					continue
				}
				child.real = target
			} else if !e.IsDir() {
				continue
			}
			queue = append(queue, child)
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		di, dj := packDepth(found[i]), packDepth(found[j])
		if di != dj {
			return di < dj
		}
		return found[i].BaseDir < found[j].BaseDir
	})
	// Within the pack the first (shallowest, then smallest BaseDir) keeps a name.
	winner := map[string]string{}
	for i := range found {
		if by, ok := winner[found[i].Name]; ok {
			found[i].ShadowedBy = by
			continue
		}
		winner[found[i].Name] = found[i].BaseDir
	}
	out := found[:0:0]
	var shadowed []Skill
	for _, s := range found {
		if s.ShadowedBy != "" {
			shadowed = append(shadowed, s)
		} else {
			out = append(out, s)
		}
	}
	return append(out, shadowed...), problems
}

// packDepth is how many directory levels below its pack directory a pack
// skill sits.
func packDepth(s Skill) int {
	return strings.Count(s.BaseDir, "/")
}

// lenientCheck is the frontmatter check for skills found inside packs: a name
// and a description are required, the strict naming rules are not.
func lenientCheck(s Skill) error {
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("frontmatter has no name")
	}
	if strings.TrimSpace(s.Description) == "" {
		return errors.New("frontmatter has no description")
	}
	if !lenientName(s.Name) {
		return fmt.Errorf("unusable skill name %q", s.Name)
	}
	return nil
}

// lenientName accepts any name that can be typed as one token and stored as a
// selection key: no whitespace, control characters or path separators.
func lenientName(n string) bool {
	if n == "" || len(n) > 128 || strings.HasPrefix(n, ".") {
		return false
	}
	for _, r := range n {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

// validPackID accepts a pack key: one existing-style directory name.
func validPackID(n string) bool {
	return n != "" && len(n) <= 255 && packCandidate(n) && lenientName(n) && !strings.ContainsAny(n, ":\x00")
}

// TruncateDescription shortens s to at most n runes, ending with "…" when it
// cut something. Whitespace runs collapse to single spaces first.
func TruncateDescription(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return strings.TrimRight(string(r[:n-1]), " ") + "…"
}
