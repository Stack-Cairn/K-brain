package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type trustedFile struct {
	Paths map[string]bool `json:"paths"`
}

func trustedPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "trusted.json"), nil
}

func Trusted(dir string) bool {
	p, err := trustedPath()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	var t trustedFile
	if json.Unmarshal(data, &t) != nil {
		return false
	}
	return t.Paths[dir]
}

func Trust(dir string) error {
	p, err := trustedPath()
	if err != nil {
		return err
	}
	t := trustedFile{Paths: map[string]bool{}}
	if data, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(data, &t)
	}
	if t.Paths == nil {
		t.Paths = map[string]bool{}
	}
	t.Paths[dir] = true
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	LogEvent("trust.grant", dir)
	return nil
}
