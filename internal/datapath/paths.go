package datapath

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	LiveAgentHomeEnv = "LIVEAGENT_HOME"
	LegacyHomeEnv    = "K_BRAIN_HOME"
	LiveAgentDirName = ".liveagent"
	LegacyDirName    = ".k-brain"
)

// UserDir resolves the global data directory and imports legacy entries without
// replacing files already present in the new directory.
func UserDir() (string, error) {
	home, _ := os.UserHomeDir()
	preferred, legacy := os.Getenv(LiveAgentHomeEnv), os.Getenv(LegacyHomeEnv)
	if testing.Testing() {
		home, preferred, legacy = testSafe(home, preferred, legacy)
	}
	return Resolve(home, preferred, legacy)
}

// LegacyUserDir is the pre-LiveAgent global directory (~/.k-brain) that is
// imported once. Under `go test` it is "" unless the home directory is a
// temporary one: tests must not read a developer's real data.
func LegacyUserDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	dir := filepath.Join(home, LegacyDirName)
	if testing.Testing() && !InTempDir(dir) {
		return ""
	}
	return dir
}

func Resolve(home, preferred, legacyOverride string) (string, error) {
	if dir := strings.TrimSpace(preferred); dir != "" {
		return ensureUserDir(dir)
	}
	if dir := strings.TrimSpace(legacyOverride); dir != "" {
		return ensureUserDir(dir)
	}
	if home == "" {
		return "", fmt.Errorf("user home is unavailable")
	}
	dir := filepath.Join(home, LiveAgentDirName)
	legacy := filepath.Join(home, LegacyDirName)
	if err := Migrate(legacy, dir); err != nil {
		return "", err
	}
	return ensureUserDir(dir)
}

func ensureUserDir(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create LiveAgent data directory %q: %w", dir, err)
	}
	return dir, nil
}

// ProjectDir returns the project-scoped data directory. It uses .liveagent
// exclusively; .k-brain remains available for one-way prompt compatibility.
func ProjectDir(project string) string { return filepath.Join(project, LiveAgentDirName) }

func LegacyProjectDir(project string) string { return filepath.Join(project, LegacyDirName) }

func MigrateProjectDir(project string) error {
	return Migrate(LegacyProjectDir(project), ProjectDir(project))
}

// Migrate imports a legacy directory once. Completed imports are not replayed
// after users delete or archive data in the destination. Root symlinks are rejected;
// links inside the source are copied verbatim without following them.
func Migrate(src, dst string) error {
	info, err := os.Lstat(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect legacy directory %q: %w", src, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("legacy path %q is not a directory", src)
	}
	if target, err := os.Lstat(dst); err == nil {
		if !target.IsDir() {
			return fmt.Errorf("migration destination %q must be a directory, not a symlink", dst)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	marker := filepath.Join(dst, fmt.Sprintf(".migration-%x", sha256.Sum256([]byte(filepath.Clean(src)))))
	if info, err := os.Lstat(marker); err == nil {
		if info.Mode().IsRegular() {
			return nil
		}
		return fmt.Errorf("migration marker %q is not a regular file", marker)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return fmt.Errorf("create migration destination %q: %w", dst, err)
	}
	if info, err := os.Lstat(dst); err != nil || !info.IsDir() {
		return fmt.Errorf("migration destination %q must be a directory, not a symlink", dst)
	}
	if err := copyMissing(src, dst); err != nil {
		return fmt.Errorf("migrate %q to %q: %w", src, dst, err)
	}
	return publish(marker, strings.NewReader("complete\n"), 0o600)
}

func copyMissing(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		from, to := filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())
		info, err := os.Lstat(from)
		if err != nil {
			return err
		}
		target, err := os.Lstat(to)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			// Lstat deliberately treats a destination symlink as a leaf. Never
			// recurse through or replace a user-created destination link.
			if info.IsDir() && target.IsDir() {
				if err := copyMissing(from, to); err != nil {
					return err
				}
			}
			continue
		}
		switch {
		case info.IsDir():
			created, err := ensureDir(to, info.Mode().Perm())
			if err != nil {
				return err
			}
			if err := copyMissing(from, to); err != nil {
				return err
			}
			if created {
				if err := os.Chmod(to, info.Mode().Perm()); err != nil {
					return err
				}
			}
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(from)
			if err != nil {
				return err
			}
			if err := os.Symlink(link, to); err != nil && !os.IsExist(err) {
				return err
			}
		case info.Mode().IsRegular():
			f, err := os.Open(from)
			if err != nil {
				return err
			}
			err = publish(to, f, info.Mode().Perm())
			closeErr := f.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("unsupported legacy file %q (%s)", from, info.Mode())
		}
	}
	return nil
}

// Concurrent migrators may create the same directory. Recheck with Lstat so
// a competing file or symlink is never traversed.
func ensureDir(path string, mode os.FileMode) (bool, error) {
	if err := os.Mkdir(path, mode.Perm()|0o700); err == nil {
		return true, nil
	} else if !os.IsExist(err) {
		return false, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("migration target %q is not a directory", path)
	}
	return false, nil
}

// publish stages a complete file and publishes it with an exclusive hard link.
// A competing migrator wins or loses atomically; neither can expose a partial file.
func publish(dst string, r io.Reader, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(dst), ".liveagent-migrate-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if _, err = io.Copy(f, r); err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Link(temp, dst); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}

// Report keeps migration failures visible in discovery APIs without error returns.
func Report(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "LiveAgent directory:", err)
	}
}
