package fileuri

import (
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

func FromPath(path string) string {
	if path == "" || strings.ContainsRune(path, '\x00') {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	abs = filepath.ToSlash(abs)
	if runtime.GOOS == "windows" && strings.HasPrefix(abs, "//") {
		host, rest, _ := strings.Cut(abs[2:], "/")
		if host == "." || host == "?" {
			return ""
		}
		return (&url.URL{Scheme: "file", Host: host, Path: "/" + rest}).String()
	}
	if !strings.HasPrefix(abs, "/") {
		abs = "/" + abs
	}
	return (&url.URL{Scheme: "file", Path: abs}).String()
}

func Path(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || !strings.EqualFold(u.Scheme, "file") || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.ContainsAny(uri, "#\\") || strings.ContainsRune(u.Path, '\x00') {
		return ""
	}
	path := u.Path
	remote := u.Host != "" && !strings.EqualFold(u.Host, "localhost")
	if runtime.GOOS == "windows" {
		if strings.ContainsRune(path, '\\') {
			return ""
		}
		if remote {
			if u.Host == "." || strings.ContainsAny(u.Host, ":\\") || !strings.HasPrefix(path, "/") {
				return ""
			}
			path = "//" + u.Host + path
		} else if len(path) >= 4 && path[0] == '/' && path[2] == ':' && path[3] == '/' {
			path = path[1:]
		} else {
			return ""
		}
	} else if remote {
		return ""
	}
	path = filepath.FromSlash(path)
	if !filepath.IsAbs(path) {
		return ""
	}
	return filepath.Clean(path)
}
