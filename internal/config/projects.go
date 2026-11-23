package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const DefaultGoalMaxRounds = 100

type projectsFile struct {
	GoalMaxRounds map[string]int `json:"goalMaxRounds,omitempty"`
	// PermissionModes is the permission mode last chosen in each folder.
	PermissionModes map[string]string `json:"permissionModes,omitempty"`
}

func projectsPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "projects.json"), nil
}

func loadProjects() projectsFile {
	var f projectsFile
	p, err := projectsPath()
	if err != nil {
		return f
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return f
	}
	_ = json.Unmarshal(data, &f)
	return f
}

func (f projectsFile) save() error {
	p, err := projectsPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
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
	return nil
}

func ProjectGoalMaxRounds(dir string) int {
	return loadProjects().GoalMaxRounds[dir]
}

func SetProjectGoalMaxRounds(dir string, n int) error {
	f := loadProjects()
	if f.GoalMaxRounds == nil {
		f.GoalMaxRounds = map[string]int{}
	}
	if n > 0 {
		f.GoalMaxRounds[dir] = n
	} else {
		delete(f.GoalMaxRounds, dir)
	}
	return f.save()
}

// ProjectPermissionMode is the permission mode last chosen in dir, or "".
func ProjectPermissionMode(dir string) string {
	return loadProjects().PermissionModes[dir]
}

// SetProjectPermissionMode saves mode as dir's permission mode; "" forgets it.
func SetProjectPermissionMode(dir, mode string) error {
	f := loadProjects()
	if f.PermissionModes == nil {
		f.PermissionModes = map[string]string{}
	}
	if mode != "" {
		f.PermissionModes[dir] = mode
	} else {
		delete(f.PermissionModes, dir)
	}
	return f.save()
}
