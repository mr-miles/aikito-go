package workspace

import (
	"path/filepath"
	"testing"
)

// resolvedTempDir is t.TempDir() with symlinks resolved. Production code
// resolves paths the way Python's Path.resolve() does, so on macOS (where
// temp dirs live under /var -> /private/var, and /home links into
// /System/Volumes/Data) a test comparing against an unresolved path fails
// even though the code is right.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
