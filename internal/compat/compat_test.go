package compat

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestIsWindows(t *testing.T) {
	if got, want := IsWindows(), runtime.GOOS == "windows"; got != want {
		t.Errorf("IsWindows() = %v, want %v", got, want)
	}
}

func TestCanSymlinkMemoizedAndResettable(t *testing.T) {
	ResetSymlinkCacheForTest()
	first := CanSymlink()
	if runtime.GOOS != "windows" && !first {
		t.Fatal("CanSymlink() = false on a POSIX host")
	}
	if second := CanSymlink(); second != first {
		t.Errorf("memoized CanSymlink() changed: %v then %v", first, second)
	}

	// After reset the probe runs again; force it to fail by pointing the
	// temp dir at a path that can't be created, then restore.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing", "dir"))
	t.Setenv("TMP", filepath.Join(t.TempDir(), "missing", "dir"))
	t.Setenv("TEMP", filepath.Join(t.TempDir(), "missing", "dir"))
	ResetSymlinkCacheForTest()
	if CanSymlink() {
		t.Error("CanSymlink() = true when the probe directory can't be created; reset did not re-run the probe")
	}
	ResetSymlinkCacheForTest()
	t.Cleanup(ResetSymlinkCacheForTest)
}

func TestSecureDirectoryPermissionsCreatesMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	if err := SecureDirectoryPermissions(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("directory not created: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Errorf("created dir mode = %o, want no group/other bits", info.Mode().Perm())
	}
}

func TestSecureDirectoryPermissionsTightensExisting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := SecureDirectoryPermissions(dir); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(dir)
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("mode = %o, want 700", got)
	}
}

func TestSecureDirectoryPermissionsLeavesPrivateDirAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SecureDirectoryPermissions(dir); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(dir)
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("mode = %o, want 700", got)
	}
}

func TestSecureDirectoryPermissionsRejectsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SecureDirectoryPermissions(path); err == nil {
		t.Fatal("expected an error for a regular file")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if got := info.Mode().Perm(); got != 0o644 {
			t.Errorf("file mode changed to %o", got)
		}
	}
}

func TestSecureFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := SecureFilePermissions(path); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("mode = %o, want 600", got)
		}
		if err := SecureFilePermissions(filepath.Join(t.TempDir(), "missing")); err == nil {
			t.Error("expected an error for a missing file")
		}
	}
}

func TestIsDirectoryCaseSensitive(t *testing.T) {
	dir := t.TempDir()
	got := IsDirectoryCaseSensitive(dir)
	if runtime.GOOS == "linux" && !got {
		t.Error("IsDirectoryCaseSensitive() = false on Linux tmpfs/ext4")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("probe left files behind: %v", entries)
	}
}

func TestIsDirectoryCaseSensitiveUnwritableDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if got, want := IsDirectoryCaseSensitive(missing), runtime.GOOS != "windows"; got != want {
		t.Errorf("IsDirectoryCaseSensitive(missing) = %v, want fallback %v", got, want)
	}
}
