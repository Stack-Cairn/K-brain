package permissions

import "strings"

// macOSPrivateDirs are the top-level directories macOS makes symlinks into
// /private. /var/folders/x and /private/var/folders/x are one file, and tools
// may hand the gate either spelling: the temp directory the OS reports, or
// the path after symlinks are resolved.
var macOSPrivateDirs = []string{"/var", "/tmp", "/etc"}

// systemPathAliases returns target and, on macOS, its other spelling across
// the fixed /private symlinks, so a rule written either way matches both.
// Only these system links are followed; user symlinks are not.
func systemPathAliases(target, goos string) []string {
	if goos != "darwin" {
		return []string{target}
	}
	for _, dir := range macOSPrivateDirs {
		if under(target, "/private"+dir) {
			return []string{target, strings.TrimPrefix(target, "/private")}
		}
		if under(target, dir) {
			return []string{target, "/private" + target}
		}
	}
	return []string{target}
}

func under(target, dir string) bool {
	return target == dir || strings.HasPrefix(target, dir+"/")
}
