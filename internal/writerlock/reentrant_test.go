package writerlock

import (
	"testing"
	"time"
)

// WorkspaceWriterLock is re-entrant within a process (add skill --sync
// holds it while project sync takes it again). A second flock on a new
// descriptor would block forever instead.
func TestAcquireIsReentrant(t *testing.T) {
	home := t.TempDir()
	outer, err := Acquire(home)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		inner, err := Acquire(home)
		if err == nil {
			inner.Release()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("nested Acquire: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nested Acquire blocked: the lock is not re-entrant")
	}
	outer.Release()

	// Fully released: a fresh Acquire works, and a nested lock for another
	// state store is refused while one is held.
	again, err := Acquire(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(t.TempDir()); err == nil {
		t.Error("nested lock for a different home was allowed")
	}
	again.Release()
}
