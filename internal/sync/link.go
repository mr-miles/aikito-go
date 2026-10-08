// Symlink-based resource materialization: global skills and global
// instructions both work by symlinking a single canonical workspace path
// (a skill directory, or global/AGENTS.md) into each configured agent's
// native location — not a content-diff/copy. Ported from the SPIRIT of
// aikito/link.py + aikito/global_skills.py + aikito/instructions.py, but
// deliberately NOT a full port of their Target/consumer-dedup model:
//
// Python's real architecture funnels every agent sharing one physical
// skills_path (6 of the 8 built-in agents all declare ".agents/skills")
// through a single shared "container" directory plus resolve_targets'
// physical-identity dedup, so a shared path gets exactly one real entry
// and a "SHARED_PATH" (no-op) observation for every other agent pointing
// at it. resolve_targets needs compat.py's get_physical_path, which this
// Go port has explicitly deferred (see internal/registry/targets_todo.go).
//
// This file instead plans and applies one independent symlink per
// (agent, resource) pair. For agents that happen to share a physical path,
// this still converges correctly — once the first agent's plan creates the
// symlink, every other agent sharing that exact path observes it already
// present and pointing at canonical, so it classifies as NOOP — but it
// does NOT implement the managed-container/legacy-migration/orphan-cleanup
// machinery Python's version has. Revisit once resolve_targets exists.
package sync

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mr-miles/aikito-rs/internal/compat"
)

type LinkAction string

const (
	LinkCreate   LinkAction = "CREATE"
	LinkNoop     LinkAction = "NOOP"
	LinkConflict LinkAction = "CONFLICT"
	LinkSkip     LinkAction = "SKIP"
)

// LinkOperation is one planned (or already-observed) symlink target.
type LinkOperation struct {
	Action        LinkAction
	TargetPath    string // absolute
	CanonicalPath string // absolute
	ResourceName  string // e.g. skill name, or "" for a single-file instructions link
	Agent         string
	Reason        string
	RequiresForce bool // true if Action==CONFLICT would otherwise block, but --force authorizes CREATE
	IsAuthorized  bool
}

// PlanSymlink classifies the current state of targetPath against
// canonicalPath: missing -> CREATE; a symlink already resolving to
// canonicalPath -> NOOP; anything else (a differently-targeted symlink, or
// real file/dir content) -> CONFLICT unless force is true, in which case it
// becomes an authorized CREATE with RequiresForce set (so callers can still
// report "this overwrote something" distinctly from a plain create).
func PlanSymlink(targetPath, canonicalPath, agent, resourceName string, force bool) (LinkOperation, error) {
	target, err := filepath.Abs(targetPath)
	if err != nil {
		return LinkOperation{}, err
	}
	canonical, err := filepath.Abs(canonicalPath)
	if err != nil {
		return LinkOperation{}, err
	}
	op := LinkOperation{TargetPath: target, CanonicalPath: canonical, Agent: agent, ResourceName: resourceName}

	info, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			op.Action = LinkCreate
			op.Reason = "New symlink"
			op.IsAuthorized = true
			return op, nil
		}
		return LinkOperation{}, err
	}

	if info.Mode()&os.ModeSymlink != 0 {
		raw, rerr := os.Readlink(target)
		if rerr == nil {
			resolved := raw
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(filepath.Dir(target), resolved)
			}
			resolvedAbs, rerr2 := filepath.Abs(resolved)
			if rerr2 == nil && filepath.Clean(resolvedAbs) == filepath.Clean(canonical) {
				op.Action = LinkNoop
				op.Reason = "Already linked to canonical source"
				op.IsAuthorized = true
				return op, nil
			}
		}
		op.Action = LinkConflict
		op.Reason = fmt.Sprintf("Existing symlink at %s points elsewhere; rerun with --force to replace it", target)
		if force {
			op.Action = LinkCreate
			op.RequiresForce = true
			op.IsAuthorized = true
			op.Reason = "Replacing symlink pointing elsewhere (--force)"
		}
		return op, nil
	}

	op.Action = LinkConflict
	kind := "file"
	if info.IsDir() {
		kind = "directory"
	}
	op.Reason = fmt.Sprintf("Unmanaged %s already exists at %s; rerun with --force to replace it", kind, target)
	if force {
		op.Action = LinkCreate
		op.RequiresForce = true
		op.IsAuthorized = true
		op.Reason = fmt.Sprintf("Replacing unmanaged %s (--force)", kind)
	}
	return op, nil
}

// ApplySymlink creates/replaces the symlink per a LinkOperation whose
// Action is CREATE. NOOP/CONFLICT/SKIP are no-ops here (callers decide
// whether an unauthorized CONFLICT aborts the whole sync). Falls back to a
// recursive copy when compat.CanSymlink() is false (e.g. Windows without
// Developer Mode), since this host cannot create the symlink Python's
// equivalent host would — the result is a point-in-time snapshot rather
// than a live link, so re-running sync after the canonical content changes
// will re-copy rather than automatically reflect the change.
func ApplySymlink(op LinkOperation) error {
	if op.Action != LinkCreate {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(op.TargetPath), 0o777); err != nil {
		return err
	}
	if _, err := os.Lstat(op.TargetPath); err == nil {
		if err := os.RemoveAll(op.TargetPath); err != nil {
			return err
		}
	}
	if compat.CanSymlink() {
		return os.Symlink(op.CanonicalPath, op.TargetPath)
	}
	return copyPath(op.CanonicalPath, op.TargetPath)
}

func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, info.Mode().Perm())
	}
	if err := os.MkdirAll(dst, 0o777); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := copyPath(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
