package sandbox

import (
	"path"
	"strings"
)

// secretEnvPatterns match the names of variables that commonly carry
// credentials. A sandboxed command does not inherit them: the credential
// directories are hidden from it, and the same keys in the environment would
// otherwise reach it anyway. Matching ignores case.
var secretEnvPatterns = []string{
	"*KEY*", "*SECRET*", "*TOKEN*", "*PASSWORD*", "*PASSWD*", "*CREDENTIAL*", "*_PAT",
	// Loader injection carried over from the backend's own environment.
	"LD_PRELOAD", "LD_AUDIT", "DYLD_INSERT_LIBRARIES",
}

// FilterEnv returns env without the variables a sandboxed command must not
// inherit, unless their names match p.EnvKeep. env is returned as is when the
// policy is off.
func (p *Policy) FilterEnv(env []string) []string {
	if !p.Enabled() {
		return env
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if matchEnv(secretEnvPatterns, name) && !matchEnv(p.EnvKeep, name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func matchEnv(patterns []string, name string) bool {
	upper := strings.ToUpper(name)
	for _, pattern := range patterns {
		if ok, err := path.Match(strings.ToUpper(strings.TrimSpace(pattern)), upper); err == nil && ok {
			return true
		}
	}
	return false
}
