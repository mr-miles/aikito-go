package sync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mr-miles/aikito-rs/internal/workspace"
)

// buildMinimalWorkspace creates the smallest tree that
// workspace.SnapshotWorkspace accepts without findings: the v2 layout
// marker plus the required empty agents/subagents/mcps/skills/memory/
// global directories and files.
func buildMinimalWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{"agents", "subagents", "mcps", "skills", "memory", "projects"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("layout.toml", "version = 2\n")
	write("skills.toml", "skills = []\n")
	write("global/AGENTS.md", "# Global\n")
	return root
}

func fullPolicy() PathPolicy {
	return PathPolicy{
		CreateParents: true,
		States:        nil,
	}
}

// TestApplyResourceWritesCreatesMemoryNote is a true end-to-end test of the
// glue this fork built: PrepareResourceWrites composes a Change from a
// ResourceWrite pointing at new content in a separate "source" tree, then
// ApplyResourceWrites commits it through the real transaction engine
// (internal/sync's Apply, built by a parallel fork) and verifies the result
// via a real workspace.SnapshotWorkspace re-scan (built by another parallel
// fork) — i.e. this exercises all three forks' work wired together, not
// just this file's code in isolation.
func TestApplyResourceWritesCreatesMemoryNote(t *testing.T) {
	target := buildMinimalWorkspace(t)
	source := buildMinimalWorkspace(t)

	noteContent := "# New note\nSomething worth remembering.\n"
	notePath := filepath.Join(source, "memory", "note.md")
	if err := os.WriteFile(notePath, []byte(noteContent), 0o644); err != nil {
		t.Fatal(err)
	}

	sourceSnapshot, err := workspace.SnapshotWorkspace(source, source)
	if err != nil {
		t.Fatalf("source snapshot: %v", err)
	}
	targetSnapshot, err := workspace.SnapshotWorkspace(target, target)
	if err != nil {
		t.Fatalf("target snapshot: %v", err)
	}

	noteResource, ok := sourceSnapshot.Resources["memory:note.md"]
	if !ok {
		t.Fatalf("expected memory:note.md in source snapshot, got %v", sourceSnapshot.Resources)
	}

	fp := noteResource.Fingerprint
	writes := []ResourceWrite{
		{
			RelativePath: "memory/note.md",
			Kind:         "memory",
			Name:         "note.md",
			Fingerprint:  &fp,
			Before:       nil,
		},
	}

	policy := fullPolicy()
	if err := ApplyResourceWrites(sourceSnapshot, targetSnapshot, writes, policy, nil, target, nil); err != nil {
		t.Fatalf("ApplyResourceWrites: %v", err)
	}

	gotContent, err := os.ReadFile(filepath.Join(target, "memory", "note.md"))
	if err != nil {
		t.Fatalf("expected memory/note.md to exist on disk: %v", err)
	}
	if string(gotContent) != noteContent {
		t.Errorf("written content = %q, want %q", gotContent, noteContent)
	}

	after, err := workspace.SnapshotWorkspace(target, target)
	if err != nil {
		t.Fatalf("post-write snapshot: %v", err)
	}
	if _, ok := after.Resources["memory:note.md"]; !ok {
		t.Errorf("expected memory:note.md in post-write snapshot, got %v", after.Resources)
	}
}

// TestApplyResourceWritesRejectsStaleSource exercises the compare-and-swap
// guard: if the claimed source fingerprint no longer matches the source
// snapshot's recorded resource, PrepareResourceWrites must abort before
// touching the target filesystem at all.
func TestApplyResourceWritesRejectsStaleSource(t *testing.T) {
	target := buildMinimalWorkspace(t)
	source := buildMinimalWorkspace(t)
	if err := os.WriteFile(filepath.Join(source, "memory", "note.md"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceSnapshot, err := workspace.SnapshotWorkspace(source, source)
	if err != nil {
		t.Fatal(err)
	}
	targetSnapshot, err := workspace.SnapshotWorkspace(target, target)
	if err != nil {
		t.Fatal(err)
	}

	staleFp := "0000000000000000000000000000000000000000000000000000000000000000000000000"[:64]
	writes := []ResourceWrite{
		{RelativePath: "memory/note.md", Kind: "memory", Name: "note.md", Fingerprint: &staleFp},
	}

	err = ApplyResourceWrites(sourceSnapshot, targetSnapshot, writes, fullPolicy(), nil, target, nil)
	if err == nil {
		t.Fatal("expected an error for a stale source fingerprint")
	}
	if _, statErr := os.Stat(filepath.Join(target, "memory", "note.md")); !os.IsNotExist(statErr) {
		t.Errorf("expected no file written to target after a rejected write, stat err = %v", statErr)
	}
}

func TestPartitionWrites(t *testing.T) {
	writes := []ResourceWrite{
		{RelativePath: "memory/a.md", Kind: "memory"},
		{RelativePath: "config.toml", Kind: "config", Name: "memory.stale_days"},
		{RelativePath: "memory/b.md", Kind: "memory"},
	}
	copies, merges := PartitionWrites(writes)
	if len(copies) != 2 || copies[0] != "memory/a.md" || copies[1] != "memory/b.md" {
		t.Errorf("copies = %v, want [memory/a.md memory/b.md]", copies)
	}
	if len(merges) != 1 || merges[0].Kind != "config" {
		t.Errorf("merges = %v, want 1 config write", merges)
	}
}

func TestMissingReferencesExcludesBundledSkills(t *testing.T) {
	resources := map[string]workspace.Resource{
		"project-skill:proj1/aikito": {
			Kind: "project-skill", Name: "proj1/aikito",
			References: []string{"skill:aikito"},
		},
		"project-skill:proj1/missing": {
			Kind: "project-skill", Name: "proj1/missing",
			References: []string{"skill:missing"},
		},
	}
	got := MissingReferences(resources, nil)
	if len(got) != 1 || got[0] != "project-skill:proj1/missing references missing skill:missing" {
		t.Errorf("MissingReferences = %v, want exactly one finding about skill:missing", got)
	}
}
