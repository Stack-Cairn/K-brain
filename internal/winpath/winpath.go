// Package winpath maps the Win32 file namespace spelling of a local path to the
// ordinary one, so that path boundaries and permission rules compare one form.
//
// \\?\D:\a\b.png names the same file as D:\a\b.png; it only tells Windows to skip
// path normalization. Tools such as Rust's fs::canonicalize and screenshot helpers
// print it, and models copy it. Go treats \\?\D: and D: as different volumes, so
// filepath.Rel between the two spellings fails.
package winpath

import (
	"errors"
	"runtime"
	"strings"
)

// ErrNamespace reports a Windows namespace spelling (\\.\, \??\, volume GUIDs,
// GLOBALROOT, ...) or an extended path that has no ordinary equivalent.
var ErrNamespace = errors.New(`unsupported Windows path namespace; use an ordinary path such as D:\dir\file or \\server\share\file`)

// Normalize returns the ordinary spelling of an extended local path: \\?\x:\dir
// becomes X:\dir and \\?\UNC\server\share\dir becomes \\server\share\dir. Paths
// without a namespace prefix, and every path off Windows, come back unchanged.
// Device namespaces, volume paths, and extended paths whose meaning would change
// once Windows normalizes them (".", "..", "/", empty components, trailing dots or
// spaces, ":" streams, reserved device names) return ErrNamespace. Long paths need
// no prefix: the os package adds one itself.
func Normalize(p string) (string, error) {
	if runtime.GOOS != "windows" || !hasNamespacePrefix(p) {
		return p, nil
	}
	if p[2] != '?' || p[0] != '\\' || p[1] != '\\' || p[3] != '\\' {
		return "", ErrNamespace
	}
	rest := p[4:]
	if len(rest) >= 4 && strings.EqualFold(rest[:4], `UNC\`) {
		// server\share\..., both required.
		if !ordinaryComponents(strings.Split(rest[4:], `\`), 2) {
			return "", ErrNamespace
		}
		return `\\` + rest[4:], nil
	}
	if len(rest) < 3 || !isDriveLetter(rest[0]) || rest[1] != ':' || rest[2] != '\\' {
		return "", ErrNamespace
	}
	if !ordinaryComponents(strings.Split(rest[3:], `\`), 0) {
		return "", ErrNamespace
	}
	return strings.ToUpper(rest[:1]) + rest[1:], nil
}

// ordinaryComponents checks the components after a volume. The first required of
// them must be present; a single trailing separator on a directory is allowed.
func ordinaryComponents(components []string, required int) bool {
	if len(components) < required {
		return false
	}
	for i, c := range components {
		if c == "" && i == len(components)-1 && i >= required {
			continue
		}
		if !ordinaryComponent(c) {
			return false
		}
	}
	return true
}

// Simplify is Normalize for paths the backend already trusts, such as configured
// roots: a spelling Normalize refuses is returned unchanged.
func Simplify(p string) string {
	if n, err := Normalize(p); err == nil {
		return n
	}
	return p
}

// hasNamespacePrefix matches \\?\, \\.\ and \??\ with either separator.
func hasNamespacePrefix(p string) bool {
	if len(p) < 4 || !isSeparator(p[3]) {
		return false
	}
	if isSeparator(p[0]) && isSeparator(p[1]) && (p[2] == '?' || p[2] == '.') {
		return true
	}
	return isSeparator(p[0]) && p[1] == '?' && p[2] == '?'
}

func ordinaryComponent(c string) bool {
	if c == "" || c == "." || c == ".." {
		return false
	}
	if last := c[len(c)-1]; last == '.' || last == ' ' {
		return false
	}
	for i := 0; i < len(c); i++ {
		if b := c[i]; b < 0x20 || strings.IndexByte(`/:*?"<>|`, b) >= 0 {
			return false
		}
	}
	return !reservedName(c)
}

// reservedName reports DOS device names, which Win32 resolves to devices in any
// directory and with any extension (nul.png is NUL).
func reservedName(c string) bool {
	base := strings.ToUpper(strings.TrimRight(strings.SplitN(c, ".", 2)[0], " "))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if len(base) >= 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) {
		switch base[3:] {
		case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
			return true
		}
	}
	return false
}

func isDriveLetter(b byte) bool { return 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' }

func isSeparator(b byte) bool { return b == '\\' || b == '/' }
