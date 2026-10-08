// Package writerlock ports skill_state.py's WorkspaceWriterLock: an
// exclusive, per-home lock file at
// ~/.local/state/aikito/project-skills/writer.lock that serialises
// workspace mutations between aikito processes.
package writerlock

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mr-miles/aikito-go/internal/compat"
)

// StateDir is skill_state.py's get_skill_state_dir.
func StateDir(home string) string {
	return filepath.Join(home, ".local", "state", "aikito", "project-skills")
}

func isSymlink(p string) bool {
	info, err := os.Lstat(p)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

// validateStateRoot ports validate_state_store_root(create_if_missing=True):
// create the state dir (0700), then walk up to home rejecting symlinked
// components and tightening group/world-writable ones.
func validateStateRoot(home string) (string, error) {
	stateDir := StateDir(home)
	if _, err := os.Stat(stateDir); err != nil {
		if err := os.MkdirAll(stateDir, 0o777); err != nil {
			return stateDir, fmt.Errorf("Failed to create state directory %s: %v", stateDir, err)
		}
		if !compat.IsWindows() {
			_ = os.Chmod(stateDir, 0o700)
		}
	}
	for curr := stateDir; curr != home && filepath.Dir(curr) != curr; curr = filepath.Dir(curr) {
		if isSymlink(curr) {
			return stateDir, fmt.Errorf("State directory component is a symbolic link or reparse point: %s", curr)
		}
		if compat.IsWindows() {
			continue
		}
		info, err := os.Stat(curr)
		if err != nil {
			continue
		}
		if info.Mode().Perm()&0o022 != 0 {
			_ = os.Chmod(curr, 0o700)
			if info, err = os.Stat(curr); err == nil && info.Mode().Perm()&0o022 != 0 {
				return stateDir, fmt.Errorf("Insecure permissions on state directory %s: %#o", curr, info.Mode().Perm())
			}
		}
	}
	return stateDir, nil
}

// Lock is a held writer lock.
type Lock struct{ f *os.File }

// Acquire blocks until this process holds the writer lock for home.
func Acquire(home string) (*Lock, error) {
	if resolved, err := compat.ResolvePath(home); err == nil {
		home = resolved
	}
	stateDir, err := validateStateRoot(home)
	if err != nil {
		return nil, fmt.Errorf("Failed to validate state store root %s: %v", stateDir, err)
	}
	lockPath := filepath.Join(stateDir, "writer.lock")
	if isSymlink(lockPath) {
		return nil, fmt.Errorf("Writer lock file is a reparse point or symlink: %s", lockPath)
	}
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o666)
	if err != nil {
		return nil, err
	}
	if err := compat.SecureFilePermissions(lockPath); err != nil {
		f.Close()
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Release unlocks and closes the lock file.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = unlockFile(l.f)
	_ = l.f.Close()
	l.f = nil
}
