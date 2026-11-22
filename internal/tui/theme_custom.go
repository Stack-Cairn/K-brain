package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/muesli/termenv"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// Custom themes are JSON files in the themes directory under the kn config
// directory (~/.liveagent/themes/*.json). Each one names a built-in base and
// overrides any of its tokens per variant:
//
//	{
//	  "name": "ocean",                 // optional; defaults to the file name
//	  "description": "deep blue",      // optional
//	  "base": "harbor",                // optional; a built-in family, "default" if omitted
//	  "dark":  {"accent": "#4fc3f7", "state.ok": {"hex": "#7fd18b", "x256": 114, "ansi": 10}},
//	  "light": {"accent": "#0277bd"}
//	}
//
// A token's value is either "#rrggbb" or an object with the hex colour and,
// optionally, the 256-colour (16..255) and 16-colour (0..15) indexes to use
// instead of the computed nearest ones. The theme has the variants its base
// has. A file that does not parse or validate is skipped with an error that
// names the file and the field; contrast below the built-in themes' minimums
// is only a warning, shown in the picker.

// themesDirName is the directory under config.Dir() that holds theme files.
const themesDirName = "themes"

// maxThemeFile bounds the size of one theme file.
const maxThemeFile = 64 << 10

// The custom theme state, like the token table, is only touched from the
// UI goroutine (startup, Update), so it needs no lock.
var (
	// customThemeErrs are the files the last load skipped, one line each.
	customThemeErrs []string
	// customThemeSig is the directory signature the last load saw.
	customThemeSig string
)

// themeFile is the on-disk schema.
type themeFile struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description"`
	Base        string                     `json:"base"`
	Dark        map[string]json.RawMessage `json:"dark"`
	Light       map[string]json.RawMessage `json:"light"`
}

// themeColorFile is the object form of one token's colour.
type themeColorFile struct {
	Hex  string `json:"hex"`
	X256 *int   `json:"x256"`
	ANSI *int   `json:"ansi"`
}

var (
	themeNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	hexColorRE  = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

// themesDir is where theme files live.
func themesDir() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, themesDirName), nil
}

// themeDirSignature is a cheap fingerprint of the theme files: names, sizes
// and modification times. It changes when a file is added, removed or saved.
func themeDirSignature(dir string) string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, e := range ents {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%s|%d|%d\n", e.Name(), info.Size(), info.ModTime().UnixNano())
	}
	return b.String()
}

// reloadCustomThemes reads the theme files again when the directory changed
// since the last load (or force is set) and reports whether anything was
// reloaded.
func reloadCustomThemes(force bool) bool {
	dir, err := themesDir()
	if err != nil {
		return false
	}
	sig := themeDirSignature(dir)
	if !force && sig == customThemeSig {
		return false
	}
	customThemeSig = sig
	customThemes, customThemeErrs = loadCustomThemes(dir)
	return true
}

// customThemeErrors is the error line for every file the last load skipped.
func customThemeErrors() []string {
	return slices.Clone(customThemeErrs)
}

// loadCustomThemes parses every *.json file in dir, in name order. A file
// that fails is skipped and reported; a later file cannot take a name an
// earlier one (or a built-in) already has.
func loadCustomThemes(dir string) ([]*themeDef, []string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, []string{fmt.Sprintf("%s: %v", themesDirName, err)}
	}
	var (
		themes []*themeDef
		errs   []string
	)
	taken := map[string]bool{}
	for _, e := range ents {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			continue
		}
		rel := filepath.Join(themesDirName, e.Name())
		data, err := readThemeFile(filepath.Join(dir, e.Name()))
		if err == nil {
			var t *themeDef
			if t, err = parseThemeFile(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())), data); err == nil {
				if taken[t.name] {
					err = fmt.Errorf("name %q is already used by another theme file", t.name)
				} else {
					taken[t.name] = true
					t.source = rel
					themes = append(themes, t)
				}
			}
		}
		if err != nil {
			errs = append(errs, rel+": "+err.Error())
		}
	}
	return themes, errs
}

func readThemeFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxThemeFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxThemeFile {
		return nil, fmt.Errorf("file is larger than %d KB", maxThemeFile>>10)
	}
	return data, nil
}

// parseThemeFile validates one theme file; stem names the theme when the
// file does not.
func parseThemeFile(stem string, data []byte) (*themeDef, error) {
	var f themeFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("invalid JSON: %v", err)
	}
	if dec.More() {
		return nil, errors.New("invalid JSON: more than one value in the file")
	}
	name := strings.ToLower(strings.TrimSpace(f.Name))
	if name == "" {
		name = strings.ToLower(stem)
	}
	if !themeNameRE.MatchString(name) {
		return nil, fmt.Errorf("name %q: use 1-32 lower-case letters, digits and dashes", name)
	}
	switch name {
	case "auto", "light", "dark", "mono", defaultThemeName:
		return nil, fmt.Errorf("name %q is reserved", name)
	}
	for _, b := range builtinThemes {
		if b.name == name {
			return nil, fmt.Errorf("name %q is a built-in theme", name)
		}
	}
	baseName := strings.ToLower(strings.TrimSpace(f.Base))
	if baseName == "" {
		baseName = defaultThemeName
	}
	var base *themeDef
	for _, b := range builtinThemes {
		if b.name == baseName {
			base = b
		}
	}
	if base == nil {
		names := make([]string, len(builtinThemes))
		for i, b := range builtinThemes {
			names[i] = b.name
		}
		return nil, fmt.Errorf("base %q is not a built-in theme (one of %s)", f.Base, strings.Join(names, ", "))
	}
	t := &themeDef{name: name, desc: strings.TrimSpace(f.Description), highContrast: base.highContrast}
	if t.desc == "" {
		t.desc = "based on " + base.name
	}
	for _, v := range []struct {
		label     string
		light     bool
		raw       map[string]json.RawMessage
		base, out **themePalette
	}{
		{"dark", false, f.Dark, &base.dark, &t.dark},
		{"light", true, f.Light, &base.light, &t.light},
	} {
		if *v.base == nil {
			if len(v.raw) > 0 {
				return nil, fmt.Errorf("%s: base %q has no %s variant", v.label, base.name, v.label)
			}
			continue
		}
		o, err := parseThemeOverrides(v.label, v.light, v.raw)
		if err != nil {
			return nil, err
		}
		*v.out = overlay(*v.base, o)
	}
	t.warnings = themeContrastWarnings(t)
	return t, nil
}

// parseThemeOverrides validates one variant's token map.
func parseThemeOverrides(label string, light bool, raw map[string]json.RawMessage) (themeOverrides, error) {
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	o := themeOverrides{}
	for _, k := range keys {
		id, ok := tokenByName(k)
		if !ok {
			return nil, fmt.Errorf("%s.%s: unknown token (known: %s)", label, k, strings.Join(themeTokenNames(), ", "))
		}
		c, err := parseThemeColor(raw[k], defaultTokens[id].surface, light)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %v", label, k, err)
		}
		o[id] = c
	}
	return o, nil
}

// themeTokenNames are the tokens a theme file may set: every token but body
// text, which always keeps the terminal's own foreground.
func themeTokenNames() []string {
	var out []string
	for _, d := range defaultTokens {
		if !d.termDefault {
			out = append(out, d.name)
		}
	}
	return out
}

// tokenByName finds a settable token.
func tokenByName(name string) (tokenID, bool) {
	for id, d := range defaultTokens {
		if d.name == name && !d.termDefault {
			return tokenID(id), true
		}
	}
	return 0, false
}

// parseThemeColor reads "#rrggbb" or {"hex", "x256", "ansi"}. Missing
// fallbacks are computed: the nearest 256-colour cube or grey entry, and for
// a foreground the nearest 16-colour slot that is not the usual background.
// Surfaces never tint a 16-colour terminal.
func parseThemeColor(raw json.RawMessage, surface, light bool) (themeColor, error) {
	var cf themeColorFile
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		cf.Hex = s
	} else {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cf); err != nil {
			return themeColor{}, errors.New(`want "#rrggbb" or {"hex": "#rrggbb", "x256": 0-255, "ansi": 0-15}`)
		}
	}
	if !hexColorRE.MatchString(cf.Hex) {
		return themeColor{}, fmt.Errorf("%q is not a #rrggbb colour", cf.Hex)
	}
	c := themeColor{hex: strings.ToLower(cf.Hex)}
	if cf.X256 != nil {
		if *cf.X256 < 0 || *cf.X256 > 255 {
			return themeColor{}, fmt.Errorf("x256 %d is not in 0..255", *cf.X256)
		}
		c.x256 = strconv.Itoa(*cf.X256)
	} else {
		c.x256 = nearest256(c.hex)
	}
	switch {
	case surface && cf.ANSI != nil:
		return themeColor{}, errors.New("a surface takes no ansi value: backgrounds are not tinted on 16-colour terminals")
	case surface:
	case cf.ANSI != nil:
		if *cf.ANSI < 0 || *cf.ANSI > 15 {
			return themeColor{}, fmt.Errorf("ansi %d is not in 0..15", *cf.ANSI)
		}
		c.ansi = strconv.Itoa(*cf.ANSI)
	default:
		c.ansi = nearest16(c.hex, light)
	}
	return c, nil
}

// nearest256 is the closest xterm cube or grey-ramp index to a colour.
func nearest256(hex string) string {
	if c, ok := termenv.ANSI256.Convert(termenv.RGBColor(hex)).(termenv.ANSI256Color); ok {
		return strconv.Itoa(int(c))
	}
	return "7"
}

// xterm16 is the xterm default RGB of the 16 base colours.
var xterm16 = [16][3]float64{
	{0, 0, 0}, {205, 0, 0}, {0, 205, 0}, {205, 205, 0}, {0, 0, 238}, {205, 0, 205}, {0, 205, 205}, {229, 229, 229},
	{127, 127, 127}, {255, 0, 0}, {0, 255, 0}, {255, 255, 0}, {92, 92, 255}, {255, 0, 255}, {0, 255, 255}, {255, 255, 255},
}

// nearest16 is the closest 16-colour slot to a foreground colour, never the
// slot that usually is the background (0 on dark terminals, 7 and 15 on
// light ones).
func nearest16(hex string, light bool) string {
	r, g, b, _ := hexRGB(hex)
	best, bestD := 8, math.Inf(1)
	for i, c := range xterm16 {
		if (!light && i == 0) || (light && (i == 7 || i == 15)) {
			continue
		}
		dr, dg, db := c[0]-r, c[1]-g, c[2]-b
		if d := 2*dr*dr + 4*dg*dg + 3*db*db; d < bestD {
			best, bestD = i, d
		}
	}
	return strconv.Itoa(best)
}

func hexRGB(hex string) (r, g, b float64, ok bool) {
	var ri, gi, bi int
	if _, err := fmt.Sscanf(hex, "#%02x%02x%02x", &ri, &gi, &bi); err != nil {
		return 0, 0, 0, false
	}
	return float64(ri), float64(gi), float64(bi), true
}

// luminance is the WCAG relative luminance of a #rrggbb colour.
func luminance(hex string) float64 {
	r, g, b, _ := hexRGB(hex)
	lin := func(v float64) float64 {
		c := v / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

// contrastRatio is the WCAG contrast ratio of two #rrggbb colours.
func contrastRatio(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

// themeContrastWarnings checks a custom theme against the rules the built-in
// themes are tested by (theme_palette_test.go): text at 4.5:1 and other
// signals at 3:1 on a typical background of the variant's polarity (7:1 and
// 4.5:1 for a high-contrast base), and secondary text on the band surface.
func themeContrastWarnings(t *themeDef) []string {
	var out []string
	for _, v := range []struct {
		label string
		p     *themePalette
		bg    string
	}{{"dark", t.dark, "#1e1e1e"}, {"light", t.light, "#ffffff"}} {
		if v.p == nil {
			continue
		}
		textMin, otherMin := 4.5, 3.0
		if t.highContrast {
			textMin, otherMin = 7, 4.5
		}
		for id, d := range defaultTokens {
			if d.termDefault || d.surface || tokenID(id) == tokLineRule || tokenID(id) == tokChipInk || strings.HasPrefix(d.name, "logo.") {
				continue
			}
			minimum := otherMin
			if strings.HasPrefix(d.name, "text.") {
				minimum = textMin
			}
			if got := contrastRatio(v.p[id].hex, v.bg); got < minimum {
				out = append(out, fmt.Sprintf("%s %s %s is %.1f:1 on a %s background (want %.1f:1)", v.label, d.name, v.p[id].hex, got, v.label, minimum))
			}
		}
		for _, id := range []tokenID{tokTextSecondary, tokTextMuted} {
			if got := contrastRatio(v.p[id].hex, v.p[tokSurfaceBand].hex); got < 4.5 {
				out = append(out, fmt.Sprintf("%s %s is %.1f:1 on surface.band (want 4.5:1)", v.label, defaultTokens[id].name, got))
			}
		}
	}
	return out
}

// refreshCustomThemes reloads the theme files when they changed (always when
// force is set), reports files that newly fail to load, and, if the active or
// saved theme is a custom one, re-applies the saved theme so an edit shows at
// once (and a removed theme falls back to the default).
func (m *model) refreshCustomThemes(force bool) {
	prevErrs := customThemeErrors()
	if !reloadCustomThemes(force) {
		return
	}
	if errs := customThemeErrors(); len(errs) > 0 && !slices.Equal(errs, prevErrs) {
		for _, e := range errs {
			m.append(warnStyle.Render(chromeGlyphs(glyphs.Warn + " theme file skipped: " + e)))
		}
	}
	theme := ""
	if m.cfg != nil {
		theme = m.cfg.Theme
	}
	family, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(theme)), ":")
	saved := lookupTheme(family)
	// activeTheme may point at a family that was just replaced or removed.
	if (activeTheme != nil && activeTheme.source != "") || (saved != nil && saved.source != "") {
		m.themeHow = m.applyTheme(theme)
		setMonoMode(wantMono(os.Getenv, theme), stdoutIsTTY())
		m.restyleTranscript()
	}
}
