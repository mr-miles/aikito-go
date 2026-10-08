//go:build !windows

package writerlock

import (
	"os"
	"path/filepath"
	"testing"
)

// Matches skill_state.py: the state dir is 0700, the lock file 0600, and
// the lock can be taken again once released.
func TestAcquireCreatesSecureStateStore(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		lock, err := Acquire(home)
		if err != nil {
			t.Fatal(err)
		}
		lock.Release()
	}
	if info, err := os.Stat(StateDir(home)); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("state dir: %v %v", info.Mode().Perm(), err)
	}
	if info, err := os.Stat(filepath.Join(StateDir(home), "writer.lock")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("lock file: %v %v", info.Mode().Perm(), err)
	}
}

func TestAcquireRejectsSymlinkedStateComponent(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(home, "elsewhere")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(home, ".local")); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(home); err == nil {
		t.Fatal("expected an error for a symlinked ~/.local")
	}
}
