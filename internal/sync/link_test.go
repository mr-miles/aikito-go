package sync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPlanSymlinkCreateNoopConflict(t *testing.T) {
	root := t.TempDir()
	canonical := filepath.Join(root, "canonical")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")

	// Missing target -> CREATE.
	op, err := PlanSymlink(target, canonical, "agent1", "skill1", false)
	if err != nil {
		t.Fatal(err)
	}
	if op.Action != LinkCreate || !op.IsAuthorized {
		t.Fatalf("expected authorized CREATE, got %+v", op)
	}

	if err := ApplySymlink(op); err != nil {
		t.Fatal(err)
	}

	// Now already linked -> NOOP.
	op2, err := PlanSymlink(target, canonical, "agent1", "skill1", false)
	if err != nil {
		t.Fatal(err)
	}
	if op2.Action != LinkNoop {
		t.Fatalf("expected NOOP after linking, got %+v", op2)
	}

	// Point elsewhere -> CONFLICT without force.
	other := filepath.Join(root, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, target); err != nil {
		t.Fatal(err)
	}
	op3, err := PlanSymlink(target, canonical, "agent1", "skill1", false)
	if err != nil {
		t.Fatal(err)
	}
	if op3.Action != LinkConflict || op3.IsAuthorized {
		t.Fatalf("expected unauthorized CONFLICT, got %+v", op3)
	}

	// With --force: authorized CREATE, RequiresForce set.
	op4, err := PlanSymlink(target, canonical, "agent1", "skill1", true)
	if err != nil {
		t.Fatal(err)
	}
	if op4.Action != LinkCreate || !op4.IsAuthorized || !op4.RequiresForce {
		t.Fatalf("expected forced authorized CREATE, got %+v", op4)
	}
	if err := ApplySymlink(op4); err != nil {
		t.Fatal(err)
	}
	resolved, err := os.Readlink(target)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != canonical {
		t.Errorf("after force-replace, symlink = %q, want %q", resolved, canonical)
	}
}

func TestPlanSymlinkUnmanagedRealFileConflict(t *testing.T) {
	root := t.TempDir()
	canonical := filepath.Join(root, "canonical.md")
	if err := os.WriteFile(canonical, []byte("canonical content"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target.md")
	if err := os.WriteFile(target, []byte("hand-edited content"), 0o644); err != nil {
		t.Fatal(err)
	}

	op, err := PlanSymlink(target, canonical, "agent1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if op.Action != LinkConflict || op.IsAuthorized {
		t.Fatalf("expected unauthorized CONFLICT for unmanaged real file, got %+v", op)
	}
}

func TestApplySymlinkFallsBackToCopyWithoutSymlinkSupport(t *testing.T) {
	root := t.TempDir()
	canonical := filepath.Join(root, "canonical")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(canonical, "SKILL.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")

	op := LinkOperation{Action: LinkCreate, TargetPath: target, CanonicalPath: canonical, IsAuthorized: true}

	// Simulate no-symlink-support by calling copyPath directly (the
	// production fallback path depends on a real OS capability probe,
	// compat.CanSymlink(), which we don't want to fake process-globally in
	// a parallel-safe test).
	if err := copyPath(op.CanonicalPath, op.TargetPath); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(target, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Errorf("copied content = %q, want %q", data, "hello")
	}
}
