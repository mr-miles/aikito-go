package sync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mr-miles/aikito-rs/internal/registry"
)

func setupProjectFixture(t *testing.T) (aikitoDir, home, checkout string) {
	t.Helper()
	home = t.TempDir()
	aikitoDir = filepath.Join(home, "aikito")
	checkout = t.TempDir()

	for _, dir := range []string{"subagents", "agents", "global"} {
		if err := os.MkdirAll(filepath.Join(aikitoDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(aikitoDir, "layout.toml"), []byte("version = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(aikitoDir, "global", "AGENTS.md"), []byte("# Global\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	skillDir := filepath.Join(aikitoDir, "skills", "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# My Skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	projDir := filepath.Join(aikitoDir, "projects", "myproj")
	if err := os.MkdirAll(filepath.Join(projDir, "memory", "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "AGENTS.md"), []byte("# Project Instructions\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeTestAgentTOML(t, aikitoDir, "codex", `
[agents.codex]
display_name = "Codex"
project_instruction_path = "AGENTS.md"
`)

	return aikitoDir, home, checkout
}

// --- Link mode ---

func TestBuildProjectSkillsPlanLinkModeCreateApplyNoop(t *testing.T) {
	aikitoDir, home, checkout := setupProjectFixture(t)

	ops, err := BuildProjectSkillsPlan(aikitoDir, home, "myproj", checkout, []string{"my-skill"}, "link", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Action != "CREATE" || ops[0].Mode != "link" {
		t.Fatalf("expected 1 link CREATE, got %+v", ops)
	}
	if err := ApplyProjectSkillOperation(home, "myproj", ops[0]); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(checkout, ".agents", "skills", "my-skill")
	data, err := os.ReadFile(filepath.Join(target, "SKILL.md"))
	if err != nil {
		t.Fatalf("linked skill content unreadable: %v", err)
	}
	if string(data) != "# My Skill\n" {
		t.Errorf("content = %q", data)
	}
	if info, err := os.Lstat(target); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected %s to be a symlink", target)
	}

	// Re-plan: NOOP.
	ops2, err := BuildProjectSkillsPlan(aikitoDir, home, "myproj", checkout, []string{"my-skill"}, "link", false)
	if err != nil {
		t.Fatal(err)
	}
	if ops2[0].Action != "NOOP" {
		t.Fatalf("expected NOOP on re-plan, got %+v", ops2[0])
	}
}

func TestBuildProjectSkillsPlanLinkModeConflictAndForce(t *testing.T) {
	aikitoDir, home, checkout := setupProjectFixture(t)
	target := filepath.Join(checkout, ".agents", "skills", "my-skill")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "unmanaged.txt"), []byte("not aikito's"), 0o644); err != nil {
		t.Fatal(err)
	}

	ops, err := BuildProjectSkillsPlan(aikitoDir, home, "myproj", checkout, []string{"my-skill"}, "link", false)
	if err != nil {
		t.Fatal(err)
	}
	if ops[0].Action != "CONFLICT" || ops[0].IsAuthorized {
		t.Fatalf("expected unauthorized CONFLICT, got %+v", ops[0])
	}

	ops2, err := BuildProjectSkillsPlan(aikitoDir, home, "myproj", checkout, []string{"my-skill"}, "link", true)
	if err != nil {
		t.Fatal(err)
	}
	if ops2[0].Action != "CREATE" || !ops2[0].RequiresForce || !ops2[0].IsAuthorized {
		t.Fatalf("expected forced authorized CREATE, got %+v", ops2[0])
	}
	if err := ApplyProjectSkillOperation(home, "myproj", ops2[0]); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(target); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected --force to replace unmanaged dir with a symlink")
	}
}

// --- Copy mode ---

func TestBuildProjectSkillsPlanCopyModeCreateApplyNoop(t *testing.T) {
	aikitoDir, home, checkout := setupProjectFixture(t)

	ops, err := BuildProjectSkillsPlan(aikitoDir, home, "myproj", checkout, []string{"my-skill"}, "copy", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Action != "CREATE" || ops[0].Mode != "copy" {
		t.Fatalf("expected 1 copy CREATE, got %+v", ops)
	}
	if err := ApplyProjectSkillOperation(home, "myproj", ops[0]); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(checkout, ".agents", "skills", "my-skill")
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("expected a real directory copy, not a symlink, at %s", target)
	}
	data, err := os.ReadFile(filepath.Join(target, "SKILL.md"))
	if err != nil || string(data) != "# My Skill\n" {
		t.Fatalf("copied content = %q, err %v", data, err)
	}

	// Re-plan: NOOP (state recorded matching fingerprints).
	ops2, err := BuildProjectSkillsPlan(aikitoDir, home, "myproj", checkout, []string{"my-skill"}, "copy", false)
	if err != nil {
		t.Fatal(err)
	}
	if ops2[0].Action != "NOOP" {
		t.Fatalf("expected NOOP on re-plan, got %+v", ops2[0])
	}
}

func TestBuildProjectSkillsPlanCopyModeDriftIsConflictThenForce(t *testing.T) {
	aikitoDir, home, checkout := setupProjectFixture(t)

	ops, err := BuildProjectSkillsPlan(aikitoDir, home, "myproj", checkout, []string{"my-skill"}, "copy", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyProjectSkillOperation(home, "myproj", ops[0]); err != nil {
		t.Fatal(err)
	}

	// Simulate a project collaborator hand-editing the copy.
	target := filepath.Join(checkout, ".agents", "skills", "my-skill")
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("# Hand-edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ops2, err := BuildProjectSkillsPlan(aikitoDir, home, "myproj", checkout, []string{"my-skill"}, "copy", false)
	if err != nil {
		t.Fatal(err)
	}
	if ops2[0].Action != "CONFLICT" || ops2[0].IsAuthorized {
		t.Fatalf("expected unauthorized CONFLICT on drifted copy, got %+v", ops2[0])
	}

	// --force resolves it by overwriting.
	ops3, err := BuildProjectSkillsPlan(aikitoDir, home, "myproj", checkout, []string{"my-skill"}, "copy", true)
	if err != nil {
		t.Fatal(err)
	}
	if ops3[0].Action != "UPDATE" || !ops3[0].RequiresForce || !ops3[0].IsAuthorized {
		t.Fatalf("expected forced authorized UPDATE, got %+v", ops3[0])
	}
	if err := ApplyProjectSkillOperation(home, "myproj", ops3[0]); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(target, "SKILL.md"))
	if err != nil || string(data) != "# My Skill\n" {
		t.Fatalf("expected --force to restore canonical content, got %q (err %v)", data, err)
	}

	// Clean again: NOOP.
	ops4, err := BuildProjectSkillsPlan(aikitoDir, home, "myproj", checkout, []string{"my-skill"}, "copy", false)
	if err != nil {
		t.Fatal(err)
	}
	if ops4[0].Action != "NOOP" {
		t.Fatalf("expected NOOP after forced restore, got %+v", ops4[0])
	}
}

func TestBuildProjectSkillsPlanCopyModeCanonicalChangedIsUpdate(t *testing.T) {
	aikitoDir, home, checkout := setupProjectFixture(t)

	ops, err := BuildProjectSkillsPlan(aikitoDir, home, "myproj", checkout, []string{"my-skill"}, "copy", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyProjectSkillOperation(home, "myproj", ops[0]); err != nil {
		t.Fatal(err)
	}

	// The canonical skill itself changes (not the copy).
	skillMD := filepath.Join(aikitoDir, "skills", "my-skill", "SKILL.md")
	if err := os.WriteFile(skillMD, []byte("# Updated Skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ops2, err := BuildProjectSkillsPlan(aikitoDir, home, "myproj", checkout, []string{"my-skill"}, "copy", false)
	if err != nil {
		t.Fatal(err)
	}
	if ops2[0].Action != "UPDATE" || !ops2[0].IsAuthorized || ops2[0].RequiresForce {
		t.Fatalf("expected an unforced, authorized UPDATE when canonical changed, got %+v", ops2[0])
	}
	if err := ApplyProjectSkillOperation(home, "myproj", ops2[0]); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(checkout, ".agents", "skills", "my-skill", "SKILL.md"))
	if err != nil || string(data) != "# Updated Skill\n" {
		t.Fatalf("expected copy to be refreshed, got %q (err %v)", data, err)
	}
}

// --- State file ---

func TestProjectSkillStateRoundTrip(t *testing.T) {
	home := t.TempDir()

	empty, err := loadProjectSkillState(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Entries) != 0 {
		t.Fatalf("expected no entries for a missing state file, got %+v", empty)
	}

	s := projectSkillStateFile{Version: 1, Entries: map[string]projectSkillStateEntry{
		"myproj/my-skill": {CanonicalFingerprint: "abc123", CopyFingerprint: "abc123"},
	}}
	if err := saveProjectSkillState(home, s); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadProjectSkillState(home)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Entries["myproj/my-skill"] != s.Entries["myproj/my-skill"] {
		t.Errorf("round-tripped state = %+v, want %+v", loaded.Entries, s.Entries)
	}
}

func TestProjectSkillStateCorruptedFileReturnsError(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ProjectSkillStateFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProjectSkillState(home); err == nil {
		t.Fatal("expected an error loading a corrupted state file, got nil")
	}

	// Confirm it's valid, parseable JSON once written normally (sanity check
	// on the save path's own format, independent of the corruption case).
	s := projectSkillStateFile{Version: 1, Entries: map[string]projectSkillStateEntry{}}
	if err := saveProjectSkillState(home, s); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("saved state file is not valid JSON: %v", err)
	}
}

// --- Always-linked instructions/memory, regardless of sync_mode ---

func TestBuildProjectInstructionsPlanAlwaysLinks(t *testing.T) {
	aikitoDir, home, checkout := setupProjectFixture(t)
	reg, err := registry.LoadStrict(aikitoDir, home)
	if err != nil {
		t.Fatal(err)
	}

	// BuildProjectInstructionsPlan takes no sync_mode parameter at all: by
	// construction, project instructions can only ever be planned as a
	// symlink, regardless of whatever sync_mode the project's skills use.
	// This test locks in that it actually produces a working link.
	ops, err := BuildProjectInstructionsPlan(aikitoDir, "myproj", checkout, reg, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Action != LinkCreate {
		t.Fatalf("expected 1 CREATE, got %+v", ops)
	}
	if err := ApplySymlink(ops[0]); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(checkout, "AGENTS.md")
	info, err := os.Lstat(target)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected a symlink at %s, err %v", target, err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "# Project Instructions\n" {
		t.Fatalf("content = %q, err %v", data, err)
	}
}

func TestBuildProjectInstructionsPlanMissingCanonicalIsError(t *testing.T) {
	aikitoDir, _, checkout := setupProjectFixture(t)
	if err := os.Remove(filepath.Join(aikitoDir, "projects", "myproj", "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	reg := &registry.AgentRegistry{}
	if _, err := BuildProjectInstructionsPlan(aikitoDir, "myproj", checkout, reg, false); err == nil {
		t.Fatal("expected an error when canonical project instructions are missing")
	}
}

func TestBuildProjectMemoryPlanAlwaysLinksFlatAndNested(t *testing.T) {
	aikitoDir, _, checkout := setupProjectFixture(t)
	memDir := filepath.Join(aikitoDir, "projects", "myproj", "memory")
	if err := os.WriteFile(filepath.Join(memDir, "flat.md"), []byte("flat note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "notes", "nested.md"), []byte("nested note\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ops, err := BuildProjectMemoryPlan(aikitoDir, "myproj", checkout, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 {
		t.Fatalf("expected 2 planned memory links (flat + nested), got %d: %+v", len(ops), ops)
	}
	for _, op := range ops {
		if op.Action != LinkCreate {
			t.Fatalf("expected CREATE, got %+v", op)
		}
		if err := ApplySymlink(op); err != nil {
			t.Fatal(err)
		}
	}

	flatTarget := filepath.Join(checkout, ".agents", "memory", "flat.md")
	nestedTarget := filepath.Join(checkout, ".agents", "memory", "notes", "nested.md")
	for target, want := range map[string]string{flatTarget: "flat note\n", nestedTarget: "nested note\n"} {
		info, err := os.Lstat(target)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("expected a symlink at %s, err %v", target, err)
		}
		data, err := os.ReadFile(target)
		if err != nil || string(data) != want {
			t.Fatalf("content at %s = %q, want %q (err %v)", target, data, want, err)
		}
	}

	// Re-plan: NOOP for both.
	ops2, err := BuildProjectMemoryPlan(aikitoDir, "myproj", checkout, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops2 {
		if op.Action != LinkNoop {
			t.Fatalf("expected NOOP on re-plan, got %+v", op)
		}
	}
}

func TestBuildProjectMemoryPlanMissingDirIsEmptyNotError(t *testing.T) {
	aikitoDir, _, checkout := setupProjectFixture(t)
	ops, err := BuildProjectMemoryPlan(aikitoDir, "no-such-project", checkout, false)
	if err != nil {
		t.Fatalf("expected no error for a project with no memory dir, got %v", err)
	}
	if len(ops) != 0 {
		t.Errorf("expected no planned ops, got %+v", ops)
	}
}
