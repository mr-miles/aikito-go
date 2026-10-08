package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// ConfigCollisionError mirrors config_runtime.py's ConfigCollisionError:
// raised (not collected as a Finding) when two specs declare incompatible
// expectations about the same physical file.
type ConfigCollisionError struct{ Message string }

func (e *ConfigCollisionError) Error() string { return e.Message }

func collisionErrorf(format string, args ...any) error {
	return &ConfigCollisionError{Message: fmt.Sprintf(format, args...)}
}

// StaleConfigPlanError mirrors config_runtime.py's StaleConfigPlanError:
// something changed on disk since a plan was built, so applying it is
// refused.
type StaleConfigPlanError struct{ Message string }

func (e *StaleConfigPlanError) Error() string { return e.Message }

func staleErrorf(format string, args ...any) error {
	return &StaleConfigPlanError{Message: fmt.Sprintf(format, args...)}
}

// FileSnapshot is a minimal stand-in for config_runtime.py's FileSnapshot
// (a richer pre-image: physical identity, symlink target, size, format,
// sensitivity, is_file/is_dir — none of that framework is ported yet, owned
// by a not-yet-scoped fork). This captures just enough to detect "did the
// target file's content change since planning" for MCPFilePlan's own
// precondition check.
type FileSnapshot struct {
	Exists      bool
	ContentHash string // "" when !Exists
}

// CaptureFileSnapshot mirrors capture_file_snapshot for this package's
// narrower purpose: hash the file's current bytes, or record non-existence.
func CaptureFileSnapshot(path string) FileSnapshot {
	data, err := os.ReadFile(path)
	if err != nil {
		return FileSnapshot{Exists: false}
	}
	sum := sha256.Sum256(data)
	return FileSnapshot{Exists: true, ContentHash: hex.EncodeToString(sum[:])}
}

// ValidatePrecondition re-captures path's current snapshot and compares it
// to s, returning (true, "") if unchanged or (false, reason) if drifted.
func (s FileSnapshot) ValidatePrecondition(path string) (bool, string) {
	current := CaptureFileSnapshot(path)
	if current.Exists != s.Exists {
		if s.Exists {
			return false, "Target file was removed since the plan was generated: " + path
		}
		return false, "Target file was created since the plan was generated: " + path
	}
	if s.Exists && current.ContentHash != s.ContentHash {
		return false, "Target file content changed since the plan was generated: " + path
	}
	return true, ""
}

// ResolvePhysicalIdentity is a simplified stand-in for
// config_runtime.py's resolve_physical_identity (symlink-resolved canonical
// path, folded to lowercase when the containing directory is
// case-insensitive). The real version isn't ported yet (owned by a
// not-yet-scoped config_runtime.go fork); this resolves symlinks for
// whichever path prefix actually exists and otherwise just cleans the
// absolute path, with NO case-folding. This is sufficient for grouping
// specs that name the literal same path, but will not detect two
// differently-cased paths on a case-insensitive filesystem as the same
// physical file — revisit once config_runtime.py's physical-identity
// framework lands.
func ResolvePhysicalIdentity(path string) string {
	abs := path
	if !filepath.IsAbs(abs) {
		if cwd, err := os.Getwd(); err == nil {
			abs = filepath.Join(cwd, abs)
		}
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	// Resolve symlinks for the longest existing ancestor, same approach as
	// workspace.ResolvePath, then re-append the non-existent suffix.
	var trailing []string
	cur := abs
	for {
		if _, err := os.Lstat(cur); err == nil {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		trailing = append([]string{filepath.Base(cur)}, trailing...)
		cur = parent
	}
	resolvedBase, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return abs
	}
	return filepath.Join(append([]string{resolvedBase}, trailing...)...)
}
