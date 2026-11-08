package skills

import (
	"archive/zip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// put writes a file under root, creating parent directories.
func put(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skillMD(name, description string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\nbody\n", name, description)
}

// packFixture builds a synthetic skills root:
//
//	shared-name/                      top-level skill
//	demo-pack/                        pack (no SKILL.md of its own)
//	  README.md
//	  skills/SKILL.md                 router (SKILL.md at depth 2), "|" block-scalar description
//	  skills/radare2/SKILL.md         depth 3
//	    references/commands.md
//	  skills/reverse/ghidra-helper/SKILL.md  depth 4, frontmatter name "ghidra"
//	  skills/shared-name/             loses to the top-level skill
//	  skills/broken/                  SKILL.md without a description
//	  skills/no-frontmatter/          SKILL.md without frontmatter
//	  plugins/adapter/skills/demo-router/  client adapter dir copy, shadowed
//	  node_modules/x/, build/, .git/hooks/  skipped
//	  a/b/c/d/deep-skill/             depth 5, beyond the walk
//	  escape -> <outside>             symlink out of the root, skipped
//	zz-pack/skills/demo-router/       loses to demo-pack (pack vs pack)
//	junk/notes/                       no SKILL.md anywhere: nothing
//	.hidden-pack/x/                   dot dirs are never packs
func packFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	put(t, root, "shared-name/SKILL.md", skillMD("shared-name", "Top-level skill"))
	put(t, root, "demo-pack/README.md", "# demo")
	put(t, root, "demo-pack/skills/SKILL.md", "---\nname: demo-router\ndescription: |\n  Routes reverse engineering tasks.\n  Pick a child skill.\n---\n\nrouter\n")
	put(t, root, "demo-pack/skills/radare2/SKILL.md", skillMD("radare2", "Use radare2"))
	put(t, root, "demo-pack/skills/radare2/references/commands.md", "r2 commands")
	put(t, root, "demo-pack/skills/reverse/ghidra-helper/SKILL.md", skillMD("ghidra", "Use ghidra"))
	put(t, root, "demo-pack/skills/shared-name/SKILL.md", skillMD("shared-name", "Pack copy"))
	put(t, root, "demo-pack/skills/broken/SKILL.md", "---\nname: broken\n---\n")
	put(t, root, "demo-pack/skills/no-frontmatter/SKILL.md", "just text\n")
	put(t, root, "demo-pack/plugins/adapter/skills/demo-router/SKILL.md", skillMD("demo-router", "Adapter copy"))
	put(t, root, "demo-pack/node_modules/x/SKILL.md", skillMD("from-node-modules", "skip"))
	put(t, root, "demo-pack/build/SKILL.md", skillMD("from-build", "skip"))
	put(t, root, "demo-pack/.git/hooks/SKILL.md", skillMD("from-git", "skip"))
	put(t, root, "demo-pack/a/b/c/d/deep-skill/SKILL.md", skillMD("too-deep", "skip"))
	put(t, root, "zz-pack/skills/demo-router/SKILL.md", skillMD("demo-router", "Second pack copy"))
	put(t, root, "zz-pack/skills/zz-only/SKILL.md", skillMD("zz-only", "Only in zz-pack"))
	put(t, root, "junk/notes/readme.txt", "nothing")
	put(t, root, ".hidden-pack/x/SKILL.md", skillMD("hidden", "skip"))
	outside := t.TempDir()
	put(t, outside, "SKILL.md", skillMD("escaped", "outside the root"))
	put(t, outside, "inner/SKILL.md", skillMD("escaped-inner", "outside the root"))
	_ = os.Symlink(outside, filepath.Join(root, "demo-pack", "escape"))
	return root
}

func byBaseDir(sk []Skill) map[string]Skill {
	out := map[string]Skill{}
	for _, s := range sk {
		out[s.BaseDir] = s
	}
	return out
}

func TestScanAllFindsSkillPacks(t *testing.T) {
	root := packFixture(t)
	r := ScanAll(root)
	got := byBaseDir(r.Skills)
	want := map[string]string{
		"shared-name":                            "",
		"demo-pack/skills":                       "demo-pack",
		"demo-pack/skills/radare2":               "demo-pack",
		"demo-pack/skills/reverse/ghidra-helper": "demo-pack",
		"zz-pack/skills/zz-only":                 "zz-pack",
	}
	if len(got) != len(want) {
		t.Fatalf("skills = %v, want %v", keys(got), want)
	}
	for dir, pack := range want {
		s, ok := got[dir]
		if !ok {
			t.Fatalf("missing %s in %v", dir, keys(got))
		}
		if s.Pack != pack || s.Root != root {
			t.Fatalf("%s: pack=%q root=%q", dir, s.Pack, s.Root)
		}
		if strings.Contains(s.BaseDir, `\`) {
			t.Fatalf("baseDir %q must use forward slashes", s.BaseDir)
		}
		if s.Path != filepath.Join(root, filepath.FromSlash(dir), "SKILL.md") {
			t.Fatalf("%s: path %s", dir, s.Path)
		}
	}
	if s := got["demo-pack/skills/reverse/ghidra-helper"]; s.Name != "ghidra" {
		t.Fatalf("pack skill name comes from frontmatter, got %q", s.Name)
	}
	if s := got["demo-pack/skills"]; s.Description != "Routes reverse engineering tasks.\nPick a child skill." {
		t.Fatalf("block scalar description = %q", s.Description)
	}

	shadowed := byBaseDir(r.Shadowed)
	wantShadowed := map[string]string{
		"demo-pack/plugins/adapter/skills/demo-router": "demo-pack/skills", // within a pack: shallowest wins
		"demo-pack/skills/shared-name":                 "shared-name",      // top-level beats pack
		"zz-pack/skills/demo-router":                   "demo-pack/skills", // first pack wins
	}
	if len(shadowed) != len(wantShadowed) {
		t.Fatalf("shadowed = %v", keys(shadowed))
	}
	for dir, by := range wantShadowed {
		if shadowed[dir].ShadowedBy != by {
			t.Fatalf("%s shadowed by %q, want %q", dir, shadowed[dir].ShadowedBy, by)
		}
	}

	var problems []string
	for _, p := range r.Problems {
		rel, _ := filepath.Rel(root, p.Path)
		problems = append(problems, filepath.ToSlash(rel))
	}
	slices.Sort(problems)
	if want := []string{"demo-pack/skills/broken/SKILL.md", "demo-pack/skills/no-frontmatter/SKILL.md"}; !slices.Equal(problems, want) {
		t.Fatalf("problems = %v, want %v", problems, want)
	}
	// Scan keeps returning only usable skills.
	if n := len(Scan(root)); n != len(want) {
		t.Fatalf("Scan returned %d skills", n)
	}
}

func keys(m map[string]Skill) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestScanAllFollowsSymlinksInsideRoot(t *testing.T) {
	root := t.TempDir()
	put(t, root, "store/real-skill/SKILL.md", skillMD("real-skill", "x"))
	put(t, root, "store/real-skill/sub/SKILL.md", skillMD("sub-skill", "x"))
	if err := os.MkdirAll(filepath.Join(root, "linked-pack"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "store"), filepath.Join(root, "linked-pack", "s")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	// A loop back to the pack must not be walked twice.
	_ = os.Symlink(filepath.Join(root, "linked-pack"), filepath.Join(root, "linked-pack", "loop"))
	r := ScanAll(root)
	got := byBaseDir(r.Skills)
	if _, ok := got["linked-pack/s/real-skill"]; !ok {
		t.Fatalf("symlink inside the root not followed: %v", keys(got))
	}
	for dir := range got {
		if strings.Contains(dir, "loop") {
			t.Fatalf("loop walked: %v", keys(got))
		}
	}
}

func TestScanAllPackLimits(t *testing.T) {
	t.Run("skills", func(t *testing.T) {
		root := t.TempDir()
		for i := 0; i < PackMaxSkills+5; i++ {
			put(t, root, fmt.Sprintf("big/s%03d/SKILL.md", i), skillMD(fmt.Sprintf("s%03d", i), "x"))
		}
		r := ScanAll(root)
		if len(r.Skills) != PackMaxSkills {
			t.Fatalf("skills = %d, want %d", len(r.Skills), PackMaxSkills)
		}
		if len(r.Problems) != 1 || !strings.Contains(r.Problems[0].Err, "more than 200 skills") {
			t.Fatalf("problems = %+v", r.Problems)
		}
	})
	t.Run("dirs", func(t *testing.T) {
		root := t.TempDir()
		// The pack directory counts too, so the last of these is never visited.
		for i := 0; i < PackMaxDirs; i++ {
			if err := os.MkdirAll(filepath.Join(root, "wide", fmt.Sprintf("d%04d", i)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		put(t, root, "wide/d0000/SKILL.md", skillMD("first", "x"))
		put(t, root, fmt.Sprintf("wide/d%04d/SKILL.md", PackMaxDirs-1), skillMD("last", "x"))
		r := ScanAll(root)
		if len(r.Skills) != 1 || r.Skills[0].Name != "first" {
			t.Fatalf("skills = %+v", r.Skills)
		}
		if len(r.Problems) != 1 || !strings.Contains(r.Problems[0].Err, "5000 directories") {
			t.Fatalf("problems = %+v", r.Problems)
		}
	})
	t.Run("depth", func(t *testing.T) {
		root := t.TempDir()
		put(t, root, "p/1/2/3/4/SKILL.md", skillMD("depth-four", "x"))
		put(t, root, "p/1/2/3/4/5/SKILL.md", skillMD("depth-five", "x"))
		r := ScanAll(root)
		if len(r.Skills) != 1 || r.Skills[0].Name != "depth-four" {
			t.Fatalf("skills = %+v", r.Skills)
		}
	})
	t.Run("tie broken by baseDir", func(t *testing.T) {
		root := t.TempDir()
		put(t, root, "p/b/same/SKILL.md", skillMD("same", "b"))
		put(t, root, "p/a/same/SKILL.md", skillMD("same", "a"))
		r := ScanAll(root)
		if len(r.Skills) != 1 || r.Skills[0].BaseDir != "p/a/same" || len(r.Shadowed) != 1 || r.Shadowed[0].ShadowedBy != "p/a/same" {
			t.Fatalf("skills=%+v shadowed=%+v", r.Skills, r.Shadowed)
		}
	})
}

func TestPromptBlockGroupsPacks(t *testing.T) {
	root := packFixture(t)
	block := PromptBlock(Scan(root))
	for _, want := range []string{
		`<skill_pack name="demo-pack" location="` + filepath.Join(root, "demo-pack") + `">`,
		"<name>radare2</name>",
		"<location>" + filepath.Join(root, "demo-pack", "skills", "radare2", "SKILL.md") + "</location>",
		`<skill_pack name="zz-pack"`,
	} {
		if !strings.Contains(block, xmlEscape(want)) && !strings.Contains(block, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, block)
		}
	}
	// The top-level skill stays outside the packs, in directory order.
	if i, j, k := strings.Index(block, "<name>shared-name</name>"), strings.Index(block, "</skill_pack>"), strings.Index(block, `<skill_pack name="zz-pack"`); i < j || i > k {
		t.Fatalf("top-level skill placement:\n%s", block)
	}
	if strings.Contains(block, "<more") {
		t.Fatalf("small pack listed compactly:\n%s", block)
	}
}

func TestPromptBlockPackBudget(t *testing.T) {
	root := t.TempDir()
	long := strings.Repeat("A long description that keeps going. ", 20)
	put(t, root, "big/skills/SKILL.md", skillMD("router", "Router "+long))
	for i := 0; i < 150; i++ {
		put(t, root, fmt.Sprintf("big/skills/s%03d/SKILL.md", i), skillMD(fmt.Sprintf("child-%03d", i), long))
	}
	for i := 0; i < 30; i++ {
		put(t, root, fmt.Sprintf("big/other/o%02d/SKILL.md", i), skillMD(fmt.Sprintf("other-%02d", i), long))
	}
	put(t, root, "solo/SKILL.md", skillMD("solo", long))
	sk := Scan(root)
	if len(sk) != 182 {
		t.Fatalf("scanned %d skills", len(sk))
	}
	block := PromptBlock(sk)
	start := strings.Index(block, "  <skill_pack name=")
	end := strings.Index(block, "  </skill_pack>\n") + len("  </skill_pack>\n")
	if start < 0 || end <= start {
		t.Fatalf("no pack:\n%s", block)
	}
	if size := end - start; size > PromptPackBudget || size < PromptPackBudget-500 {
		t.Fatalf("pack takes %d bytes, budget %d", size, PromptPackBudget)
	}
	// The router is listed in full, children compactly with truncated descriptions.
	if !strings.Contains(block, "<name>router</name>") {
		t.Fatal("router not listed in full")
	}
	if !strings.Contains(block, `<skill name="child-000" dir="skills/s000">`) {
		t.Fatalf("compact child entry missing:\n%s", block[:2000])
	}
	if strings.Contains(block, `<skill name="other-00"`) {
		t.Fatal("router descendants should be listed before other pack skills")
	}
	listed := strings.Count(block[start:end], "<skill name=")
	more := fmt.Sprintf(`<more count="%d">`, 180-listed)
	if !strings.Contains(block, more) {
		t.Fatalf("missing %s (listed %d)", more, listed)
	}
	// A top-level skill keeps its full description.
	if !strings.Contains(block, "<name>solo</name>") || !strings.Contains(block, strings.TrimSpace(long)) {
		t.Fatal("top-level skill truncated")
	}
}

func TestTruncateDescription(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"a  b\n c", 10, "a b c"},
		{"abcdefghij", 5, "abcd…"},
		{"逆向工程技能路由器", 4, "逆向工…"},
		{"x", 0, ""},
	} {
		if got := TruncateDescription(tc.in, tc.n); got != tc.want {
			t.Fatalf("TruncateDescription(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

// managedPackFixture installs the synthetic fixture into a fresh managed root.
func managedPackFixture(t *testing.T) *Manager {
	t.Helper()
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	m, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	src := packFixture(t)
	for _, dir := range []string{"demo-pack", "zz-pack", "junk"} {
		if err := os.Rename(filepath.Join(src, dir), filepath.Join(m.Root(), dir)); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func TestManagerListsPackSkills(t *testing.T) {
	m := managedPackFixture(t)
	list, shadowed, invalid, err := m.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]SkillSummary{}
	for _, s := range list {
		got[s.BaseDir] = s
	}
	r := got["demo-pack/skills/radare2"]
	if r.Name != "radare2" || r.Pack != "demo-pack" || r.SkillFile != "demo-pack/skills/radare2/SKILL.md" || r.Shadowed || r.BuiltIn {
		t.Fatalf("radare2 summary = %+v", r)
	}
	if s := got["skills-creator"]; !s.BuiltIn || s.Pack != "" || s.BaseDir != "skills-creator" {
		t.Fatalf("builtin summary = %+v", s)
	}
	if len(shadowed) != 2 {
		t.Fatalf("shadowed = %+v", shadowed)
	}
	for _, s := range shadowed {
		if !s.Shadowed || s.ShadowedBy != "demo-pack/skills" {
			t.Fatalf("shadowed entry = %+v", s)
		}
	}
	if len(invalid) != 2 {
		t.Fatalf("invalid = %+v", invalid)
	}
	// List keeps one entry per name.
	names := map[string]bool{}
	for _, s := range list {
		if names[s.Name] {
			t.Fatalf("duplicate name %s", s.Name)
		}
		names[s.Name] = true
	}

	content, _, _, err := m.Read("demo-pack/skills/radare2/references/commands.md", 0, 10)
	if err != nil || content != "r2 commands" {
		t.Fatalf("read reference: %q %v", content, err)
	}
	if _, _, _, err := m.Read("demo-pack/escape/SKILL.md", 0, 10); err == nil {
		t.Fatal("read through an escaping symlink")
	}
}

func TestManagerValidateAndDeletePacks(t *testing.T) {
	m := managedPackFixture(t)
	if v := m.Validate("demo-pack"); !v.OK || v.Pack != "demo-pack" || len(v.Warnings) != 2 {
		t.Fatalf("validate pack = %+v", v)
	}
	if v := m.Validate("ghidra"); !v.OK || v.Pack != "demo-pack" {
		t.Fatalf("validate pack skill by name = %+v", v)
	}
	if v := m.Validate("demo-pack/skills/radare2"); !v.OK || v.Pack != "demo-pack" {
		t.Fatalf("validate pack skill by baseDir = %+v", v)
	}
	if v := m.Validate("junk"); v.OK {
		t.Fatalf("a directory without skills validated: %+v", v)
	}

	for _, key := range []string{"radare2", "demo-pack/skills/radare2", "demo-pack/plugins/adapter/skills/demo-router"} {
		_, err := m.Delete(key)
		if err == nil || !strings.Contains(err.Error(), `skill pack "demo-pack"`) {
			t.Fatalf("delete %s: %v", key, err)
		}
	}
	if _, err := os.Stat(filepath.Join(m.Root(), "demo-pack", "skills", "radare2", "SKILL.md")); err != nil {
		t.Fatal("refused delete removed files")
	}
	if _, err := m.Delete("demo-pack"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.Root(), "demo-pack")); !os.IsNotExist(err) {
		t.Fatal("pack still present")
	}
	if _, err := m.Delete("skills-creator"); err == nil {
		t.Fatal("builtin delete accepted")
	}
	// With demo-pack gone, zz-pack's copy of demo-router takes the name.
	list, _, _, _ := m.ListAll()
	found := false
	for _, s := range list {
		found = found || (s.Name == "demo-router" && s.Pack == "zz-pack")
	}
	if !found {
		t.Fatalf("zz-pack demo-router not promoted: %+v", list)
	}
}

func TestManagerPackEnableState(t *testing.T) {
	m := managedPackFixture(t)
	workdir := t.TempDir()
	names := func() []string {
		t.Helper()
		sk, _, err := m.Effective(workdir)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, s := range sk {
			if s.Pack != "" {
				out = append(out, s.Pack+":"+s.Name)
			}
		}
		slices.Sort(out)
		return out
	}
	// Opt-in: nothing selected, no pack skills.
	if _, err := m.SaveSettings("", SkillSettings{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if got := names(); len(got) != 0 {
		t.Fatalf("unselected pack skills on: %v", got)
	}
	// Per skill, by name.
	if _, err := m.SaveSettings("", SkillSettings{Enabled: true, Selected: []string{"radare2"}}); err != nil {
		t.Fatal(err)
	}
	if got := names(); !slices.Equal(got, []string{"demo-pack:radare2"}) {
		t.Fatalf("per-skill selection: %v", got)
	}
	// Whole pack on.
	if _, err := m.SaveSettings("", SkillSettings{Enabled: true, Packs: map[string]bool{"zz-pack": true}}); err != nil {
		t.Fatal(err)
	}
	if got := names(); !slices.Equal(got, []string{"zz-pack:zz-only"}) {
		t.Fatalf("pack on: %v", got)
	}
	// A client that does not send packs keeps the stored pack switches.
	if _, err := m.SaveSettings("", SkillSettings{Enabled: true, Selected: []string{"ghidra"}}); err != nil {
		t.Fatal(err)
	}
	if got := names(); !slices.Equal(got, []string{"demo-pack:ghidra", "zz-pack:zz-only"}) {
		t.Fatalf("packs dropped by a client without packs: %v", got)
	}
	// Pack off beats a name selection; an empty map clears.
	if _, err := m.SaveSettings("", SkillSettings{Enabled: true, Selected: []string{"ghidra"}, Packs: map[string]bool{"demo-pack": false}}); err != nil {
		t.Fatal(err)
	}
	if got := names(); len(got) != 0 {
		t.Fatalf("pack off: %v", got)
	}
	if _, err := m.SaveSettings("", SkillSettings{Enabled: true, Selected: []string{"ghidra"}, Packs: map[string]bool{}}); err != nil {
		t.Fatal(err)
	}
	if _, set, _ := m.Settings(""); set.Packs != nil {
		t.Fatalf("empty packs not cleared: %v", set.Packs)
	}
	if got := names(); !slices.Equal(got, []string{"demo-pack:ghidra"}) {
		t.Fatalf("after clearing packs: %v", got)
	}
	// Project custom mode carries its own pack switches.
	if _, err := m.SaveSettings(workdir, SkillSettings{Enabled: true, Mode: "custom", SkillNames: []string{}, Packs: map[string]bool{"demo-pack": true}}); err != nil {
		t.Fatal(err)
	}
	if got := names(); !slices.Equal(got, []string{"demo-pack:demo-router", "demo-pack:ghidra", "demo-pack:radare2", "demo-pack:shared-name"}) {
		t.Fatalf("project pack on: %v", got)
	}
	if _, err := m.SaveSettings("", SkillSettings{Enabled: true, Packs: map[string]bool{"../x": true}}); err == nil {
		t.Fatal("invalid pack key accepted")
	}
	// The prompt groups the enabled pack skills.
	sk, _, _ := m.Effective(workdir)
	if block := PromptBlock(sk); !strings.Contains(block, `<skill_pack name="demo-pack"`) {
		t.Fatalf("prompt:\n%s", block)
	}
}

func packSourceDir(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "my-pack")
	put(t, src, "README.md", "readme")
	put(t, src, ".git/config", "[core]")
	put(t, src, "skills/router/SKILL.md", skillMD("pack-router", "Router"))
	put(t, src, "skills/router/child/SKILL.md", skillMD("pack-child", "Child"))
	return src
}

func TestManagerInstallsPacks(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	m, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// A local directory without a root SKILL.md.
	res, err := m.Install(ctx, packSourceDir(t), "", "fail", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "my-pack" || res.Pack != "my-pack" || res.Skills != 2 || res.SkillFile != "my-pack/skills/router/SKILL.md" {
		t.Fatalf("install dir = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(m.Root(), "my-pack", ".git")); !os.IsNotExist(err) {
		t.Fatal("dot directory copied")
	}
	if _, err := m.Install(ctx, packSourceDir(t), "", "fail", nil, nil); err == nil {
		t.Fatal("conflict accepted")
	}

	// A zip whose single top directory holds the pack.
	archive := filepath.Join(t.TempDir(), "download.zip")
	writeZip(t, archive, map[string]string{
		"zipped-pack/skills/a/SKILL.md":   skillMD("zip-a", "A"),
		"zipped-pack/skills/a/b/SKILL.md": skillMD("zip-b", "B"),
		"zipped-pack/.github/x.yml":       "x",
	})
	if res, err = m.Install(ctx, archive, "", "fail", nil, nil); err != nil || res.Pack != "zipped-pack" || res.Skills != 2 {
		t.Fatalf("install zip = %+v %v", res, err)
	}

	// Nothing to install.
	empty := filepath.Join(t.TempDir(), "empty")
	put(t, empty, "docs/readme.md", "no skills")
	if _, err := m.Install(ctx, empty, "", "fail", nil, nil); err == nil || !strings.Contains(err.Error(), "4 levels") {
		t.Fatalf("empty source: %v", err)
	}

	list, _, _, err := m.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	var packSkills []string
	for _, s := range list {
		if s.Pack != "" {
			packSkills = append(packSkills, s.Pack+":"+s.Name)
		}
	}
	slices.Sort(packSkills)
	if want := []string{"my-pack:pack-child", "my-pack:pack-router", "zipped-pack:zip-a", "zipped-pack:zip-b"}; !slices.Equal(packSkills, want) {
		t.Fatalf("pack skills = %v", packSkills)
	}
}

func TestManagerInstallsGitHubPack(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	archive := filepath.Join(t.TempDir(), "repo.zip")
	writeZip(t, archive, map[string]string{
		"demo-skills-main/README.md":                "readme",
		"demo-skills-main/skills/router/SKILL.md":   skillMD("gh-router", "Router"),
		"demo-skills-main/skills/router/x/SKILL.md": skillMD("gh-x", "X"),
	})
	body, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	// Serve the repository archive for every request (codeload.github.com included).
	var requested string
	if err := m.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requested = r.URL.String()
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Length", fmt.Sprint(len(body)))
		_, _ = rec.Write(body)
		return rec.Result(), nil
	})}); err != nil {
		t.Fatal(err)
	}
	res, err := m.Install(context.Background(), "https://github.com/example/demo-skills", "", "fail", nil, nil)
	if err != nil {
		t.Fatalf("install: %v (requested %s)", err, requested)
	}
	if res.Pack != "demo-skills" || res.Skills != 2 || res.SkillFile != "demo-skills/skills/router/SKILL.md" {
		t.Fatalf("install = %+v", res)
	}
	if requested != "https://codeload.github.com/example/demo-skills/zip/refs/heads/main" {
		t.Fatalf("requested %s", requested)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		w, err := z.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(files[n]))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
