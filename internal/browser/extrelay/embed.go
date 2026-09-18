package extrelay

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed extension
var extensionFS embed.FS

func ExtensionDir(home string) string {
	return filepath.Join(home, ".k-brain", "browser", "extension")
}

func RelayStatePath(home string) string {
	return filepath.Join(ExtensionDir(home), "relay.json")
}

func WriteExtension(dir string) ([]string, error) {
	var written []string
	entries, err := extensionFS.ReadDir("extension")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := extensionFS.ReadFile("extension/" + e.Name())
		if err != nil {
			return written, err
		}
		dst := filepath.Join(dir, e.Name())
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			return written, err
		}
		written = append(written, dst)
	}
	return written, nil
}

func WriteRelayState(home, addr, token string) (string, error) {
	p := RelayStatePath(home)
	body := fmt.Sprintf("{\"addr\":%q,\"token\":%q}\n", addr, token)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		return "", err
	}
	return p, nil
}
