// Package projectsync ports the reference CLI's project synchronization
// engine: project_sync.py and the modules it drives (skill_plan.py,
// skill_runtime.py, skill_state.py, the project parts of instructions.py
// and memory_runtime.py, link.py, conflict.py).
//
// Output lines, messages and on-disk formats (the host-local skill state
// documents and transaction journals under
// ~/.local/state/aikito/project-skills) match Python byte for byte, so the
// two implementations can be used interchangeably on one machine.
package projectsync

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mr-miles/aikito-go/internal/compat"
)

// Out carries the two streams Python's print() calls write to.
type Out struct {
	Stdout io.Writer
	Stderr io.Writer
}

func (o Out) println(format string, args ...any) {
	fmt.Fprintf(o.Stdout, format+"\n", args...)
}

func (o Out) eprintln(format string, args ...any) {
	fmt.Fprintf(o.Stderr, format+"\n", args...)
}

// Path predicates with pathlib semantics.

func isSymlink(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode()&os.ModeSymlink != 0
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func lexists(p string) bool { return isSymlink(p) || exists(p) }

// isReparsePoint is compat.is_reparse_point; on POSIX that is is_symlink.
func isReparsePoint(p string) bool { return isSymlink(p) }

// resolve is Path.resolve(strict=False).
func resolve(p string) string { return compat.PhysicalPath(p) }

// physical is compat.get_physical_path.
func physical(p string) string { return compat.PhysicalPath(p) }

// normcase is os.path.normcase: identity on POSIX, lower-case on Windows.
func normcase(p string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(strings.ReplaceAll(p, "/", `\`))
	}
	return p
}

// isRelativeTo is PurePath.is_relative_to (component-wise, no resolution).
func isRelativeTo(p, base string) bool {
	p = filepath.Clean(p)
	base = filepath.Clean(base)
	if p == base {
		return true
	}
	if base == string(filepath.Separator) {
		return strings.HasPrefix(p, base)
	}
	return strings.HasPrefix(p, base+string(filepath.Separator))
}

// joinRaw is `parent / raw` for a readlink value: absolute values replace.
func joinRaw(parent, raw string) string {
	if filepath.IsAbs(raw) {
		return raw
	}
	return filepath.Join(parent, raw)
}

// resolveSymlinkTarget is compat.resolve_symlink_target.
func resolveSymlinkTarget(p string) string {
	var target string
	if raw, err := os.Readlink(p); err == nil {
		if strings.HasPrefix(raw, `\\?\UNC\`) {
			raw = `\\` + raw[8:]
		} else if strings.HasPrefix(raw, `\\?\`) {
			raw = raw[4:]
		}
		target = joinRaw(filepath.Dir(p), raw)
	} else {
		target = resolve(p)
	}
	var parts []string
	curr := target
	for !exists(curr) && filepath.Dir(curr) != curr {
		parts = append(parts, filepath.Base(curr))
		curr = filepath.Dir(curr)
	}
	resolved := curr
	if r, err := filepath.EvalSymlinks(curr); err == nil {
		resolved = r
	} else if r, err := compat.ResolvePath(curr); err == nil {
		resolved = r
	}
	for i := len(parts) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, parts[i])
	}
	return resolved
}

// isSameTargetLocation is compat.is_same_target_location.
func isSameTargetLocation(p1, p2 string) bool {
	d1 := physical(filepath.Dir(p1))
	d2 := physical(filepath.Dir(p2))
	probe := d1
	for !exists(probe) && filepath.Dir(probe) != probe {
		probe = filepath.Dir(probe)
	}
	folds := compat.IsWindows()
	if exists(probe) {
		folds = compat.DirectoryFoldsCase(probe)
	}
	if folds {
		return strings.EqualFold(d1, d2) && strings.EqualFold(filepath.Base(p1), filepath.Base(p2))
	}
	return d1 == d2 && filepath.Base(p1) == filepath.Base(p2)
}

// safeSymlink is compat.safe_symlink: target -> source, reporting failures
// on stderr.
func safeSymlink(out Out, source, target string) bool {
	if err := os.Symlink(source, target); err != nil {
		out.eprintln("[ERROR] Failed to create symlink %s -> %s: %s", target, source, pyOSError(err))
		return false
	}
	return true
}

// safeRelativePath is compat.safe_relative_path (pure, no resolution).
func safeRelativePath(path, base string) string {
	if isRelativeTo(path, base) {
		rel, err := filepath.Rel(base, path)
		if err == nil {
			if rel == "." {
				return "~/."
			}
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(path)
}

// asPosix is PurePath.as_posix.
func asPosix(p string) string { return filepath.ToSlash(p) }

// pyOSError renders an OSError the way str(exc) does in Python:
// "[Errno N] Description: 'path'".
func pyOSError(err error) string {
	return err.Error()
}
