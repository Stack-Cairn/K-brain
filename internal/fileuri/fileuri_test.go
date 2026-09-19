package fileuri

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNativePathsRoundTrip(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"main.go", "space name.png", "截图% #1.png", "100%/a%20b.go"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		uri := FromPath(path)
		if !strings.HasPrefix(uri, "file:///") || strings.ContainsAny(uri, "\\ #") {
			t.Errorf("invalid file URI %q", uri)
		}
		if got := Path(uri); got != path {
			t.Errorf("round trip %q = %q via %q", path, got, uri)
		}
		local := strings.Replace(uri, "file:///", "file://LOCALHOST/", 1)
		if got := Path(local); got != path {
			t.Errorf("localhost round trip %q = %q", local, got)
		}
	}
}

func TestFileURIRejectsAmbiguousInput(t *testing.T) {
	for _, uri := range []string{"", "https://example.com/file", "file:relative.png", "file:///tmp/%ZZ", "file:///tmp/%00", "file:///tmp/a?query", "file:///tmp/a?", "file:///tmp/a#fragment", "file:///tmp/a#", "file://user@localhost/tmp/a", `file:///C:\tmp\a`} {
		if got := Path(uri); got != "" {
			t.Errorf("accepted %q as %q", uri, got)
		}
	}
	if FromPath("") != "" || FromPath("\x00") != "" {
		t.Fatal("invalid path converted to URI")
	}
}

func TestFileURIPlatformPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		for _, uri := range []string{"file:////remote/share/a", "file://localhost//remote/share/a", "file://./pipe/name", "file:///C:%5Ctmp%5Ca"} {
			if got := Path(uri); got != "" {
				t.Errorf("ambiguous Windows URI %q mapped to %q", uri, got)
			}
		}
		for _, tt := range []struct{ path, uri string }{
			{`C:\Users\person\main.go`, "file:///C:/Users/person/main.go"},
			{`D:\my project\百分%.go`, "file:///D:/my%20project/%E7%99%BE%E5%88%86%25.go"},
			{`\\server\share\main.go`, "file://server/share/main.go"},
		} {
			if got := FromPath(tt.path); got != tt.uri {
				t.Errorf("URI = %q, want %q", got, tt.uri)
			}
			if got := Path(tt.uri); got != tt.path {
				t.Errorf("path = %q, want %q", got, tt.path)
			}
		}
	} else {
		if got := Path(FromPath(`/tmp/literal\name`)); got != `/tmp/literal\name` {
			t.Fatalf("literal POSIX backslash did not round trip: %q", got)
		}
		if Path("file://remote.example/tmp/a") != "" {
			t.Fatal("remote URI mapped to a local file")
		}
		if got := FromPath("/tmp/space name"); got != "file:///tmp/space%20name" {
			t.Fatal(got)
		}
	}
}
