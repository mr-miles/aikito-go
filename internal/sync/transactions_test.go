package sync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mr-miles/aikito-rs/internal/workspace"
)

func writeSourceFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "source.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func digestOf(content string) string {
	return workspace.FileDigestBytes([]byte(content))
}

func TestApplyCreateUpdateRemove(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	roots := []string{root}
	target := filepath.Join(root, "memory", "foo.md")

	// Create
	fp1 := digestOf("v1")
	src1 := writeSourceFile(t, "v1")
	if err := Apply(roots, []Change{{Target: 0, Path: "memory/foo.md", Kind: "memory", Source: src1, After: &fp1}}, nil, nil, PathPolicy{}, nil); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "v1" {
		t.Fatalf("after create: data=%q err=%v", data, err)
	}

	// Update
	fp2 := digestOf("v2")
	src2 := writeSourceFile(t, "v2")
	if err := Apply(roots, []Change{{Target: 0, Path: "memory/foo.md", Kind: "memory", Source: src2, Before: &fp1, After: &fp2}}, nil, nil, PathPolicy{}, nil); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	data, err = os.ReadFile(target)
	if err != nil || string(data) != "v2" {
		t.Fatalf("after update: data=%q err=%v", data, err)
	}

	// Remove
	if err := Apply(roots, []Change{{Target: 0, Path: "memory/foo.md", Kind: "memory", Before: &fp2}}, nil, nil, PathPolicy{}, nil); err != nil {
		t.Fatalf("remove failed: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected file removed, stat err=%v", err)
	}

	pending, err := HasPending(roots, PathPolicy{}, nil)
	if err != nil || pending {
		t.Fatalf("expected no pending transaction after clean apply, pending=%v err=%v", pending, err)
	}
}

func TestApplyRejectsStaleBeforeFingerprint(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	roots := []string{root}
	target := filepath.Join(root, "memory", "foo.md")

	fp1 := digestOf("v1")
	src1 := writeSourceFile(t, "v1")
	if err := Apply(roots, []Change{{Target: 0, Path: "memory/foo.md", Kind: "memory", Source: src1, After: &fp1}}, nil, nil, PathPolicy{}, nil); err != nil {
		t.Fatal(err)
	}

	// Simulate a concurrent external edit the planner never saw.
	if err := os.WriteFile(target, []byte("concurrently-edited"), 0o644); err != nil {
		t.Fatal(err)
	}

	fp2 := digestOf("v2")
	src2 := writeSourceFile(t, "v2")
	err := Apply(roots, []Change{{Target: 0, Path: "memory/foo.md", Kind: "memory", Source: src2, Before: &fp1, After: &fp2}}, nil, nil, PathPolicy{}, nil)
	if err == nil {
		t.Fatal("expected a staleness error, got nil")
	}

	data, _ := os.ReadFile(target)
	if string(data) != "concurrently-edited" {
		t.Errorf("rejected apply must not touch the target; got %q", data)
	}
	pending, _ := HasPending(roots, PathPolicy{}, nil)
	if pending {
		t.Errorf("a staging-phase rejection must not leave a pending transaction")
	}
}

func TestApplyRejectsSourceMismatch(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	roots := []string{root}
	src := writeSourceFile(t, "actual-content")
	wrongFp := digestOf("not-the-actual-content")

	err := Apply(roots, []Change{{Target: 0, Path: "memory/foo.md", Kind: "memory", Source: src, After: &wrongFp}}, nil, nil, PathPolicy{}, nil)
	if err == nil {
		t.Fatal("expected a source-mismatch error, got nil")
	}
}

func TestApplyAutoRecoversOnVerifyFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	roots := []string{root}
	target := filepath.Join(root, "memory", "foo.md")

	fp1 := digestOf("v1")
	src1 := writeSourceFile(t, "v1")
	if err := Apply(roots, []Change{{Target: 0, Path: "memory/foo.md", Kind: "memory", Source: src1, After: &fp1}}, nil, nil, PathPolicy{}, nil); err != nil {
		t.Fatal(err)
	}

	fp2 := digestOf("v2")
	src2 := writeSourceFile(t, "v2")
	verifyErr := coreErrorf("simulated post-rename invariant failure")
	err := Apply(roots, []Change{{Target: 0, Path: "memory/foo.md", Kind: "memory", Source: src2, Before: &fp1, After: &fp2}}, nil,
		func() error { return verifyErr }, PathPolicy{}, nil)
	if err == nil {
		t.Fatal("expected Apply to surface the verify error")
	}

	// The file must have been renamed to v2 then rolled back to v1 by the
	// automatic Recover() triggered after the verify callback failed — this
	// exercises the real rename-then-rollback path, not a mock.
	data, rerr := os.ReadFile(target)
	if rerr != nil || string(data) != "v1" {
		t.Fatalf("expected rollback to v1, got data=%q err=%v", data, rerr)
	}
	pending, _ := HasPending(roots, PathPolicy{}, nil)
	if pending {
		t.Errorf("expected no pending transaction after automatic recovery")
	}
}

// TestRecoverBetweenMovesCrash manually constructs a journal in the exact
// "crashed between the two renames of the apply phase" state — the target
// already moved to tx/moved/, the new content already staged, neither rename
// into final position has happened — and verifies Recover restores the
// original content via the fast "moved" path rather than the backup copy.
func TestRecoverBetweenMovesCrash(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	roots := []string{root}
	target := filepath.Join(root, "memory", "foo.md")
	if err := os.WriteFile(target, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	fp1 := digestOf("v1")
	fp2 := digestOf("v2")

	txid := randomHex(16)
	tx, err := txDir(root, txid, true)
	if err != nil {
		t.Fatal(err)
	}

	// Stage the new content (mirrors Apply's staging phase).
	stage := filepath.Join(tx, "stage", "memory", "foo.md")
	src2 := writeSourceFile(t, "v2")
	if err := copyResource(src2, stage, "memory"); err != nil {
		t.Fatal(err)
	}
	// Back up the old content (mirrors Apply's staging phase).
	backup := filepath.Join(tx, "backup", "memory", "foo.md")
	if err := copyResource(target, backup, "memory"); err != nil {
		t.Fatal(err)
	}

	// Write the pending journal (mirrors Apply's journal-write step).
	jd := journalData{
		Version: 2,
		Roots:   roots,
		TxID:    txid,
		Phase:   "pending",
		Changes: []journalChange{{
			Target: 0, Path: "memory/foo.md", Destination: target,
			Kind: "memory", Before: &fp1, After: &fp2,
		}},
		Policy: policyToJournal(PathPolicy{}),
	}
	data, err := json.Marshal(jd)
	if err != nil {
		t.Fatal(err)
	}
	jpath, _, err := journalPath(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicText(jpath, string(data)); err != nil {
		t.Fatal(err)
	}

	// Simulate the crash landing exactly between the two renames of the
	// apply phase: move the live target out to tx/moved/ (as Apply's apply
	// phase would have done) and then stop — never rename stage -> target.
	moved := filepath.Join(tx, "moved", "memory", "foo.md")
	if err := os.MkdirAll(filepath.Dir(moved), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, moved); err != nil {
		t.Fatal(err)
	}

	ok, err := Recover(roots, PathPolicy{}, nil)
	if err != nil {
		t.Fatalf("Recover failed: %v", err)
	}
	if !ok {
		t.Fatal("expected Recover to report a transaction was rolled back")
	}

	restored, err := os.ReadFile(target)
	if err != nil || string(restored) != "v1" {
		t.Fatalf("expected v1 restored via the moved/ fast path, got data=%q err=%v", restored, err)
	}
	pending, _ := HasPending(roots, PathPolicy{}, nil)
	if pending {
		t.Errorf("expected no pending transaction after recovery")
	}

	// Idempotency: recovering again (nothing pending) must be a safe no-op.
	ok2, err := Recover(roots, PathPolicy{}, nil)
	if err != nil {
		t.Fatalf("second Recover failed: %v", err)
	}
	if ok2 {
		t.Errorf("second Recover should report nothing to do")
	}
}

// TestRecoverCrashBeforeAnyRename constructs a journal for a brand-new
// resource whose staging completed but whose creating rename never
// happened — Recover must correctly do nothing (current already equals the
// pre-transaction "missing" state) rather than erroring or fabricating
// content, and must still clean up the journal.
func TestRecoverCrashBeforeAnyRename(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	roots := []string{root}
	target := filepath.Join(root, "memory", "new.md")
	fp := digestOf("new")

	txid := randomHex(16)
	tx, err := txDir(root, txid, true)
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(tx, "stage", "memory", "new.md")
	src := writeSourceFile(t, "new")
	if err := copyResource(src, stage, "memory"); err != nil {
		t.Fatal(err)
	}

	jd := journalData{
		Version: 2, Roots: roots, TxID: txid, Phase: "pending",
		Changes: []journalChange{{Target: 0, Path: "memory/new.md", Destination: target, Kind: "memory", After: &fp}},
		Policy:  policyToJournal(PathPolicy{}),
	}
	data, _ := json.Marshal(jd)
	jpath, _, err := journalPath(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicText(jpath, string(data)); err != nil {
		t.Fatal(err)
	}

	ok, err := Recover(roots, PathPolicy{}, nil)
	if err != nil {
		t.Fatalf("Recover failed: %v", err)
	}
	if !ok {
		t.Fatal("expected Recover to report completion (journal cleanup)")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("target should still not exist, stat err=%v", err)
	}
	pending, _ := HasPending(roots, PathPolicy{}, nil)
	if pending {
		t.Errorf("expected no pending transaction after recovery")
	}
}

func TestApplySkillDirectoryCreate(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	roots := []string{root}

	srcDir := t.TempDir()
	skillDir := filepath.Join(srcDir, "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# my skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fp, err := workspace.TreeDigest(skillDir)
	if err != nil {
		t.Fatal(err)
	}

	if err := Apply(roots, []Change{{Target: 0, Path: "skills/my-skill", Kind: "skill", Source: skillDir, After: &fp}}, nil, nil, PathPolicy{}, nil); err != nil {
		t.Fatalf("skill create failed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "skills", "my-skill", "SKILL.md"))
	if err != nil || string(got) != "# my skill\n" {
		t.Fatalf("skill content mismatch: data=%q err=%v", got, err)
	}

	// Remove it.
	if err := Apply(roots, []Change{{Target: 0, Path: "skills/my-skill", Kind: "skill", Before: &fp}}, nil, nil, PathPolicy{}, nil); err != nil {
		t.Fatalf("skill remove failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "my-skill")); !os.IsNotExist(err) {
		t.Errorf("expected skill directory removed")
	}
}

func TestApplyStateUpdate(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(".local", "state", "aikito", "my-state.json")
	if err := os.MkdirAll(filepath.Join(root, ".local", "state", "aikito"), 0o755); err != nil {
		t.Fatal(err)
	}
	roots := []string{root}
	policy := PathPolicy{States: []string{statePath}}

	if err := Apply(roots, nil, []StateUpdate{{Target: 0, Path: statePath, After: `{"v":1}`}}, nil, policy, nil); err != nil {
		t.Fatalf("state create failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, statePath))
	if err != nil || string(data) != `{"v":1}` {
		t.Fatalf("state content mismatch: %q err=%v", data, err)
	}

	before := `{"v":1}`
	if err := Apply(roots, nil, []StateUpdate{{Target: 0, Path: statePath, Before: &before, After: `{"v":2}`}}, nil, policy, nil); err != nil {
		t.Fatalf("state update failed: %v", err)
	}
	data, err = os.ReadFile(filepath.Join(root, statePath))
	if err != nil || string(data) != `{"v":2}` {
		t.Fatalf("state content mismatch after update: %q err=%v", data, err)
	}
}

func TestHasPendingAndPendingKindsEmpty(t *testing.T) {
	root := t.TempDir()
	roots := []string{root}
	pending, err := HasPending(roots, PathPolicy{}, nil)
	if err != nil || pending {
		t.Fatalf("expected no pending transaction in a fresh root, pending=%v err=%v", pending, err)
	}
	kinds, err := PendingKinds(roots)
	if err != nil || len(kinds) != 0 {
		t.Fatalf("expected no pending kinds, got %v err=%v", kinds, err)
	}
}

func TestValidateResourcePathRejectsUnsafe(t *testing.T) {
	cases := []string{"", "/abs/path", `a\b`, "a/../b", "a/./b", "a/.git/b", "a/.local/b"}
	for _, p := range cases {
		if _, err := ValidateResourcePath(p, "memory", PathPolicy{}, nil); err == nil {
			t.Errorf("ValidateResourcePath(%q) should be rejected as unsafe", p)
		}
	}
}

func TestValidateResourcePathExplicitAllowList(t *testing.T) {
	policy := PathPolicy{Resources: [][2]string{{"inbox", "weird/one-off.md"}}}
	if _, err := ValidateResourcePath("weird/one-off.md", "inbox", policy, nil); err != nil {
		t.Errorf("explicit allow-list entry should validate: %v", err)
	}
	if _, err := ValidateResourcePath("weird/one-off.md", "memory", policy, nil); err == nil {
		t.Errorf("same path with a different claimed kind should be rejected")
	}
}
