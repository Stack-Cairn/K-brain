// Package storage assembles persistent stores independently of the agent runtime.
package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/datapath"
	"github.com/Stack-Cairn/K-brain/internal/memory"
	"github.com/Stack-Cairn/K-brain/internal/resources"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

type Options struct {
	Root             string
	ConfigPath       string
	SessionDir       string
	LegacyDesktopDir string
}

// Local owns data handles, but never starts tools, schedulers, or model clients.
type Local struct {
	Root       string
	ConfigPath string
	Config     *config.Config
	Sessions   *session.Store
	Memory     *memory.Store
	Prompts    *resources.PromptStore
}

func OpenLocal(opts Options) (_ *Local, err error) {
	root := opts.Root
	if root == "" {
		root, err = config.Dir()
		if err != nil {
			return nil, err
		}
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if opts.LegacyDesktopDir != "" {
		if err := migrateDesktopData(opts.LegacyDesktopDir, root, ".migration-desktop-kbrain"); err != nil {
			return nil, err
		}
		if legacy := datapath.LegacyUserDir(); legacy != "" {
			if err := migrateDesktopData(legacy, root, ".migration-legacy-kbrain"); err != nil {
				return nil, err
			}
		}
	}
	path := opts.ConfigPath
	if path == "" {
		path = filepath.Join(root, "config.json")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := initializeConfig(path); err != nil {
		return nil, err
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		return nil, err
	}
	dir := opts.SessionDir
	if dir == "" {
		dir = filepath.Join(root, "sessions")
	}
	sessions, err := session.OpenProjectDir(dir)
	if err != nil {
		return nil, fmt.Errorf("session storage: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, sessions.Close())
		}
	}()
	mem, err := memory.OpenStore(filepath.Join(root, "memory"))
	if err != nil {
		return nil, fmt.Errorf("memory storage: %w", err)
	}
	prompts, err := resources.OpenPrompts(root)
	if err != nil {
		return nil, fmt.Errorf("prompt storage: %w", err)
	}
	return &Local{Root: root, ConfigPath: path, Config: cfg, Sessions: sessions, Memory: mem, Prompts: prompts}, nil
}

// Honor markers from the previous desktop migrator to avoid resurrecting deleted data.
func migrateDesktopData(source, root, marker string) error {
	info, err := os.Lstat(filepath.Join(root, marker))
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("migration marker %s is not a regular file", marker)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if filepath.Clean(source) == filepath.Clean(root) {
		return errors.New("legacy source must differ from data root")
	}
	return datapath.Migrate(source, root)
}

func (s *Local) SaveConfig(cfg *config.Config) error { return cfg.SaveFile(s.ConfigPath) }
func (s *Local) Close() error                        { return s.Sessions.Close() }

func initializeConfig(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("configuration must be a regular file")
		}
		return os.Chmod(path, 0600)
	}
	if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	// Publish a complete empty catalog without replacing a competing initialization.
	f, err := os.CreateTemp(filepath.Dir(path), ".config-init-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString("{}\n")
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err := os.Link(f.Name(), path); err != nil {
		if !os.IsExist(err) {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("configuration must be a regular file")
		}
		return os.Chmod(path, 0600)
	}
	return nil
}
