// Package compat provides OS-level helpers whose behavior differs between
// POSIX and Windows: symlink support probing, directory case-sensitivity
// detection, and secure file/directory permission enforcement.
package compat

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"unicode"
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
	if !info.IsDir() {
		return fmt.Errorf("not a directory: %s", dir)
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

// DirectoryFoldsCase reports whether names in dir are matched
// case-insensitively, without writing anything (unlike
// IsDirectoryCaseSensitive). As in compat.py, Linux and other POSIX systems
// are assumed case-sensitive. On macOS and Windows it checks whether dir
// itself is reachable under a case-swapped name, falling back to the
// platform default (case-insensitive) when dir's name has no letters.
func DirectoryFoldsCase(dir string) bool {
	if runtime.GOOS != "darwin" && !IsWindows() {
		return false
	}
	for cur := filepath.Clean(dir); ; cur = filepath.Dir(cur) {
		base := filepath.Base(cur)
		swapped := swapCase(base)
		if swapped != base {
			a, err1 := os.Stat(cur)
			b, err2 := os.Stat(filepath.Join(filepath.Dir(cur), swapped))
			if err1 == nil {
				return err2 == nil && os.SameFile(a, b)
			}
		}
		if filepath.Dir(cur) == cur {
			return true
		}
	}
}

func swapCase(s string) string {
	r := []rune(s)
	for i, c := range r {
		if unicode.IsUpper(c) {
			r[i] = unicode.ToLower(c)
		} else if unicode.IsLower(c) {
			r[i] = unicode.ToUpper(c)
		}
	}
	return string(r)
}

// ResolvePath approximates pathlib.Path.resolve(strict=False): make the
// path absolute (relative to the process's current working directory),
// clean it, and resolve symlinks for as much of the path as exists on disk,
// leaving any non-existent trailing components untouched.
func ResolvePath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)

	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved, nil
	}

	var trailing []string
	cur := path
	for {
		if _, err := os.Lstat(cur); err == nil {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return path, nil // nothing on this path exists at all
		}
		trailing = append([]string{filepath.Base(cur)}, trailing...)
		cur = parent
	}
	resolvedBase, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return path, nil
	}
	return filepath.Join(append([]string{resolvedBase}, trailing...)...), nil
}

// PhysicalPath is compat.py's get_physical_path. On POSIX that is
// Path.resolve(strict=False). Windows' GetFinalPathNameByHandleW drive and
// volume normalisation is not ported.
func PhysicalPath(path string) string {
	p, err := ResolvePath(path)
	if err != nil {
		return path
	}
	return p
}
