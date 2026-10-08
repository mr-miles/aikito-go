package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadAgentDocuments(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "codex.toml"), []byte(
		"[agents.codex]\ndisplay_name = \"Codex\"\ninstruction_path = \".codex/AGENTS.md\"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, ".DS_Store"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	docs, err := ReadAgentDocuments(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 agent, got %d: %v", len(docs), docs)
	}
	spec, ok := docs["codex"]
	if !ok {
		t.Fatalf("expected 'codex' agent, got %v", docs)
	}
	if spec["display_name"] != "Codex" {
		t.Errorf("display_name = %v, want Codex", spec["display_name"])
	}
}

func TestReadAgentDocumentsRejectsMismatchedTable(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// File named codex.toml but table key is "other" -> must be rejected.
	if err := os.WriteFile(filepath.Join(agentsDir, "codex.toml"), []byte(
		"[agents.other]\ndisplay_name = \"Other\"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAgentDocuments(root); err == nil {
		t.Fatal("expected an error for mismatched agent table name")
	}
}
