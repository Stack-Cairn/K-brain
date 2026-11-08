package sshhosts

import "strings"

// ProjectPathKey is the key a project's SSH host associations are stored
// under. It matches LiveAgent's project_path_key, so associations a client
// saves for a project path are found again from the run's working directory:
//
//   - Windows-like paths (drive letter, UNC, \\?\ prefix): the extended prefix
//     is dropped, backslashes become slashes, trailing slashes above the root
//     are trimmed and the result is lower-cased.
//   - Other paths: trailing slashes are trimmed (the root "/" stays).
func ProjectPathKey(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if hasExtendedPrefix(trimmed) || hasDrivePrefix(trimmed) || hasUNCPrefix(trimmed) {
		return windowsKey(trimmed)
	}
	for len(trimmed) > 1 && strings.HasSuffix(trimmed, "/") {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return trimmed
}

func windowsKey(value string) string {
	switch {
	case hasExtendedUNCPrefix(value):
		value = "//" + value[8:]
	case hasExtendedPrefix(value):
		value = value[4:]
	}
	value = strings.ReplaceAll(value, `\`, "/")
	minLen := windowsRootLen(value)
	for len(value) > minLen && strings.HasSuffix(value, "/") {
		value = value[:len(value)-1]
	}
	return strings.ToLower(value)
}

func windowsRootLen(value string) int {
	if len(value) >= 3 && isASCIIAlpha(value[0]) && value[1] == ':' && value[2] == '/' {
		return 3
	}
	if rest, ok := strings.CutPrefix(value, "//"); ok {
		parts := strings.Split(rest, "/")
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			return 2
		}
		return 2 + len(parts[0]) + 1 + len(parts[1])
	}
	return 1
}

func hasDrivePrefix(value string) bool {
	return len(value) >= 2 && isASCIIAlpha(value[0]) && value[1] == ':' && (len(value) == 2 || isSeparator(value[2]))
}

func hasUNCPrefix(value string) bool {
	if len(value) < 2 || !isSeparator(value[0]) || !isSeparator(value[1]) {
		return false
	}
	parts := strings.Split(strings.ReplaceAll(value[2:], `\`, "/"), "/")
	return len(parts) >= 2 && parts[0] != "" && parts[1] != ""
}

func hasExtendedPrefix(value string) bool {
	return len(value) >= 4 && isSeparator(value[0]) && isSeparator(value[1]) && value[2] == '?' && isSeparator(value[3])
}

func hasExtendedUNCPrefix(value string) bool {
	return len(value) >= 8 && hasExtendedPrefix(value) && strings.EqualFold(value[4:7], "UNC") && isSeparator(value[7])
}

func isSeparator(b byte) bool { return b == '/' || b == '\\' }

func isASCIIAlpha(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
