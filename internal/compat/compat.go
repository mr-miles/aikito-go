// Package compat provides OS-level helpers whose behavior differs between
// POSIX and Windows: symlink support probing, directory case-sensitivity
// detection, and secure file/directory permission enforcement.
package compat

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// IsWindows reports whether the current OS is Windows.
func IsWindows() bool {
	return runtime.GOOS == "windows"
}

var (
	symlinkOnce    sync.Once
	symlinkSupport bool
)

// CanSymlink reports whether the current process can create symlinks in a
// temp directory. The result is memoized for the process lifetime, mirroring
// the Python implementation's cache_clear()-able memoization.
func CanSymlink() bool {
	symlinkOnce.Do(func() {
		dir, err := os.MkdirTemp("", "aikito-symlink-probe-")
		if err != nil {
			symlinkSupport = false
			return
		}
		defer os.RemoveAll(dir)
		target := filepath.Join(dir, "target")
		link := filepath.Join(dir, "link")
		if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
			symlinkSupport = false
			return
		}
		symlinkSupport = os.Symlink(target, link) == nil
	})
	return symlinkSupport
}

// ResetSymlinkCacheForTest clears the memoized CanSymlink result. Test-only.
func ResetSymlinkCacheForTest() {
	symlinkOnce = sync.Once{}
}

// SecureDirectoryPermissions creates dir (and parents) with mode 0700 if
// missing, and verifies the directory is not group/world-writable.
func SecureDirectoryPermissions(dir string) error {
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !IsWindows() {
		mode := info.Mode().Perm()
		if mode&0o077 != 0 {
			if err := os.Chmod(dir, mode&^0o077); err != nil {
				return err
			}
		}
	}
	return nil
}

// SecureFilePermissions chmods path to 0600 on POSIX; a no-op on Windows
// where POSIX mode bits are not meaningful.
func SecureFilePermissions(path string) error {
	if IsWindows() {
		return nil
	}
	return os.Chmod(path, 0o600)
}

// IsDirectoryCaseSensitive probes whether dir's filesystem folds case, by
// writing a marker file and checking whether it is visible under a
// differently-cased name. dir must already exist.
func IsDirectoryCaseSensitive(dir string) bool {
	probe := filepath.Join(dir, ".aikito-case-probe-aB")
	upper := filepath.Join(dir, ".AIKITO-CASE-PROBE-AB")
	if err := os.WriteFile(probe, []byte(""), 0o600); err != nil {
		// Can't probe; assume case-sensitive (the safer default for POSIX).
		return !IsWindows()
	}
	defer os.Remove(probe)
	_, err := os.Stat(upper)
	return err != nil
}
