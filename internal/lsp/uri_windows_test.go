package lsp

import "testing"

func TestWindowsFileURI(t *testing.T) {
	for _, tt := range []struct{ name, path, uri string }{
		{"drive", `C:\Users\person\main.go`, "file:///C:/Users/person/main.go"},
		{"special", `D:\my project\百分%.go`, "file:///D:/my%20project/%E7%99%BE%E5%88%86%25.go"},
		{"UNC", `\\server\share\main.go`, "file://server/share/main.go"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := fileURI(tt.path); got != tt.uri {
				t.Fatalf("URI=%q want %q", got, tt.uri)
			}
			if got := uriPath(tt.uri); got != tt.path {
				t.Fatalf("path=%q want %q", got, tt.path)
			}
		})
	}
}
