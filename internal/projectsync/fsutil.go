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
	"github.com/mr-miles/aikito-go/internal/linkplan"
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

// resolveSymlinkTarget is compat.resolve_symlink_target.
func resolveSymlinkTarget(p string) string { return linkplan.ResolveSymlinkTarget(p) }

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
