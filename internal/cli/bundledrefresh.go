package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mr-miles/aikito-go/internal/sync"
)

// Bundled-skill refresh: ports bundled_skills.py's directory digest and
// workspace/sync.py's bundled refresh plan. `sync global` brings the
// workspace's copies of the bundled skills (aikito, durable-memory) back in
// line with the ones embedded in this binary, backing up the old copy.

type digestEntry struct {
	rel, kind string
	content   []byte
}

func digestEntries(entries []digestEntry) string {
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	h := sha256.New()
	var n [8]byte
	for _, e := range entries {
		for _, part := range [][]byte{[]byte(e.kind), []byte(e.rel), e.content} {
			binary.BigEndian.PutUint64(n[:], uint64(len(part)))
			h.Write(n[:])
			h.Write(part)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func normalizeCRLF(b []byte) []byte { return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")) }

// directoryDigest ports _directory_digest for an on-disk tree. "" means
// None (root missing, not a directory, or a symlink).
func directoryDigest(root string) string {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return ""
	}
	var entries []digestEntry
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		e := digestEntry{rel: filepath.ToSlash(rel)}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			e.kind, e.content = "link", []byte(target)
		case d.IsDir():
			e.kind = "dir"
		case d.Type().IsRegular():
			data, _ := os.ReadFile(p)
			e.kind, e.content = "file", normalizeCRLF(data)
		default:
			e.kind = "other"
		}
		entries = append(entries, e)
		return nil
	})
	return digestEntries(entries)
}

func bundledSkillFSRoot(name string) string { return "templates/skills/" + name }

// bundledSkillDigest digests the copy embedded in this binary.
func bundledSkillDigest(name string) string {
	root := bundledSkillFSRoot(name)
	if _, err := fs.Stat(templatesFS, root); err != nil {
		return ""
	}
	var entries []digestEntry
	_ = fs.WalkDir(templatesFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		e := digestEntry{rel: p[len(root)+1:]}
		if d.IsDir() {
			e.kind = "dir"
		} else {
			data, _ := templatesFS.ReadFile(p)
			e.kind, e.content = "file", normalizeCRLF(data)
		}
		entries = append(entries, e)
		return nil
	})
	return digestEntries(entries)
}

// bundledRefreshOp is BundledSkillRefreshOperation: refresh is the
// "REFRESH" action, otherwise "NOOP".
type bundledRefreshOp struct {
	name, sourceDigest, targetDigest string
	refresh                          bool
}

func (op bundledRefreshOp) action() string {
	if op.refresh {
		return "REFRESH"
	}
	return "NOOP"
}

// planBundledRefresh ports build_bundled_refresh_plan: one operation per
// bundled skill. With outdatedFn (as `sync global` passes
// outdated_bundled_skills), nothing is refreshed when <workspace>/skills is
// not a directory; without it (whole-workspace `aikito sync`), any digest
// difference, including a missing copy, is a refresh.
func planBundledRefresh(aikitoDir string, outdatedFn bool) []bundledRefreshOp {
	skillsRoot := filepath.Join(aikitoDir, "skills")
	rootIsDir := false
	if info, err := os.Stat(skillsRoot); err == nil && info.IsDir() {
		rootIsDir = true
	}
	var ops []bundledRefreshOp
	for _, name := range bundledSkillOrder {
		src := bundledSkillDigest(name)
		dst := directoryDigest(filepath.Join(skillsRoot, name))
		refresh := src != dst
		if outdatedFn && !rootIsDir {
			refresh = false
		}
		ops = append(ops, bundledRefreshOp{name: name, sourceDigest: src, targetDigest: dst, refresh: refresh})
	}
	return ops
}

// refreshedNames is BundledSkillRefreshPlan.refreshed_names.
func refreshedNames(ops []bundledRefreshOp) map[string]bool {
	out := map[string]bool{}
	for _, op := range ops {
		if op.refresh {
			out[op.name] = true
		}
	}
	return out
}

// observeBundledRefresh ports BundledSkillRefreshPlan.observe.
func observeBundledRefresh(ops []bundledRefreshOp) sync.PlanObservation {
	obs := sync.PlanObservation{CanApply: true}
	for _, op := range ops {
		effect := sync.EffectNoop
		if op.refresh {
			effect = sync.EffectUpdate
		}
		reason := "Bundled skill matches package"
		if op.refresh {
			reason = "Bundled skill diverged from package"
		}
		obs.Operations = append(obs.Operations, sync.PlanOperationView{
			ResourceType: "bundled_skill", ResourceName: op.name, Effect: effect,
			Scope: "global", Target: op.name, Reason: reason, DomainAction: op.action(), Authorized: true,
		})
	}
	return obs
}

func pythonTimestamp(t time.Time) string {
	return fmt.Sprintf("%s_%06d", t.Format("20060102_150405"), t.Nanosecond()/1000)
}

// copyTreePreserving is shutil.copytree(symlinks=True) with copy2: symlinks
// are recreated, file and directory modes kept.
func copyTreePreserving(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	case info.IsDir():
		if err := os.MkdirAll(dst, 0o777); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyTreePreserving(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return os.Chmod(dst, info.Mode().Perm())
	default:
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, info.Mode().Perm())
	}
}

func copyEmbeddedTree(root, dst string) error {
	return fs.WalkDir(templatesFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out := filepath.Join(dst, filepath.FromSlash(p[len(root):]))
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := templatesFS.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o644)
	})
}

// replaceWithBundled ports _replace_directory: stage the embedded copy next
// to target, then swap it in.
func replaceWithBundled(name, target string) error {
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o777); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(target)+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	staged := filepath.Join(staging, filepath.Base(target))
	if err := copyEmbeddedTree(bundledSkillFSRoot(name), staged); err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil {
		if info.IsDir() {
			err = os.RemoveAll(target)
		} else {
			err = os.Remove(target)
		}
		if err != nil {
			return err
		}
	}
	return os.Rename(staged, target)
}

// executeBundledRefresh ports execute_bundled_refresh_plan. The caller holds
// the writer lock for a real run.
func executeBundledRefresh(planOps []bundledRefreshOp, aikitoDir, home string, dryRun bool, stdout io.Writer) ([]string, error) {
	var ops []bundledRefreshOp
	for _, op := range planOps {
		if op.refresh {
			ops = append(ops, op)
		}
	}
	if len(ops) == 0 {
		return nil, nil
	}
	var names []string
	if dryRun {
		for _, op := range ops {
			fmt.Fprintf(stdout, "[DRY-RUN] Would refresh bundled skill: %s\n", op.name)
			names = append(names, op.name)
		}
		return names, nil
	}
	skillsRoot := filepath.Join(aikitoDir, "skills")
	backupRoot := filepath.Join(home, ".aikito", "backups", "bundled-skills_"+pythonTimestamp(time.Now()))
	for _, op := range ops {
		if bundledSkillDigest(op.name) != op.sourceDigest || directoryDigest(filepath.Join(skillsRoot, op.name)) != op.targetDigest {
			return nil, fmt.Errorf("Bundled skill '%s' state diverged from plan snapshot; re-plan required", op.name)
		}
	}
	for _, op := range ops {
		target := filepath.Join(skillsRoot, op.name)
		if _, err := os.Lstat(target); err == nil {
			backup := filepath.Join(backupRoot, op.name)
			if err := os.MkdirAll(filepath.Dir(backup), 0o777); err != nil {
				return names, fmt.Errorf("Failed to refresh bundled skill '%s': %v", op.name, err)
			}
			if err := copyTreePreserving(target, backup); err != nil {
				return names, fmt.Errorf("Failed to refresh bundled skill '%s': %v", op.name, err)
			}
			fmt.Fprintf(stdout, "[BACKUP] Bundled skill '%s': %s\n", op.name, backup)
		}
		if err := replaceWithBundled(op.name, target); err != nil {
			return names, fmt.Errorf("Failed to refresh bundled skill '%s': %v", op.name, err)
		}
		fmt.Fprintf(stdout, "[REFRESH] Bundled skill '%s' updated from installed Aikito\n", op.name)
		names = append(names, op.name)
	}
	return names, nil
}

// printBundledSkillNotice ports print_bundled_skill_notice (to stderr).
// names restricts the notice to those skills (nil = all).
func printBundledSkillNotice(aikitoDir string, stderr io.Writer, names ...string) {
	var outdated []string
	for _, op := range planBundledRefresh(aikitoDir, true) {
		if op.refresh && (names == nil || containsString(names, op.name)) {
			outdated = append(outdated, op.name)
		}
	}
	if len(outdated) > 0 {
		fmt.Fprintf(stderr, "\n[NOTICE] Bundled skill snapshot differs from the installed Aikito package: %s. Run 'aikito sync global' to refresh it.\n", strings.Join(outdated, ", "))
	}
}
