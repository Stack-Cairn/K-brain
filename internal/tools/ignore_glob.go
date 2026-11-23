package tools

import "regexp"

// IgnoreGlob compiles a slash-separated glob (*, ?, **, [abc], {a,b}) the way
// the Glob, Grep and LS tools match .gitignore patterns, so other walkers
// apply the same rules.
func IgnoreGlob(pattern string) (*regexp.Regexp, error) { return liveagentGlob(pattern) }
