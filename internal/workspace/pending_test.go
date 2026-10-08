package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func journalFile(root string) string {
	return filepath.Join(append([]string{root}, append(transactionStateParts, "pending.json")...)...)
}

func writePendingJournal(t *testing.T, root, content string) string {
	t.Helper()
	path := journalFile(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// currentLayoutRoot builds a minimal valid v2 workspace root.
func currentLayoutRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"agents", "subagents"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, LayoutFile), []byte(LayoutContent), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestPendingTransactionKinds(t *testing.T) {
	t.Run("no_state_dir", func(t *testing.T) {
		kinds, err := PendingTransactionKinds([]string{t.TempDir()})
		if err != nil || len(kinds) != 0 {
			t.Fatalf("got %v, %v", kinds, err)
		}
	})
	t.Run("state_dir_without_journal", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Dir(journalFile(root)), 0o700); err != nil {
			t.Fatal(err)
		}
		kinds, err := PendingTransactionKinds([]string{root})
		if err != nil || len(kinds) != 0 {
			t.Fatalf("got %v, %v", kinds, err)
		}
	})
	t.Run("kinds_across_roots", func(t *testing.T) {
		a, b := t.TempDir(), t.TempDir()
		writePendingJournal(t, a, `{"changes":[{"kind":"layout"},{"kind":"legacy"}]}`)
		writePendingJournal(t, b, `{"changes":[{"kind":"memory"}]}`)
		kinds, err := PendingTransactionKinds([]string{a, b})
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"layout", "legacy", "memory"} {
			if _, ok := kinds[k]; !ok {
				t.Errorf("missing kind %q in %v", k, kinds)
			}
		}
	})

	// Python indexes data["changes"] and item["kind"] directly, so these are
	// invalid journals rather than "nothing pending".
	for name, content := range map[string]string{
		"corrupted_json":      `{not json`,
		"missing_changes":     `{"txid":"x"}`,
		"changes_not_list":    `{"changes":{"kind":"layout"}}`,
		"change_without_kind": `{"changes":[{"path":"layout.toml"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := writePendingJournal(t, root, content)
			_, err := PendingTransactionKinds([]string{root})
			if err == nil || err.Error() != "Invalid workspace journal: "+path {
				t.Fatalf("want Invalid workspace journal error, got %v", err)
			}
		})
	}

	t.Run("journal_is_directory", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(journalFile(root), 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := PendingTransactionKinds([]string{root})
		if err == nil || !strings.HasPrefix(err.Error(), "Unsafe journal: ") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("symlinked_state_dir_is_unsafe", func(t *testing.T) {
		root := t.TempDir()
		real := t.TempDir()
		if err := os.Symlink(real, filepath.Join(root, ".local")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		_, err := PendingTransactionKinds([]string{root})
		if err == nil || err.Error() != "Unsafe directory: "+filepath.Join(root, ".local") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestRequireCurrentLayout(t *testing.T) {
	t.Run("current_layout_ok", func(t *testing.T) {
		if err := RequireCurrentLayout(currentLayoutRoot(t)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("marker_formatting_is_not_byte_exact", func(t *testing.T) {
		// Python parses the marker as TOML rather than comparing bytes.
		root := currentLayoutRoot(t)
		if err := os.WriteFile(filepath.Join(root, LayoutFile), []byte("version=2"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := RequireCurrentLayout(root); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("pending_layout_journal_blocks_even_a_current_layout", func(t *testing.T) {
		root := currentLayoutRoot(t)
		writePendingJournal(t, root, `{"changes":[{"kind":"legacy"},{"kind":"layout"}]}`)
		err := RequireCurrentLayout(root)
		want := "Workspace migration is incomplete: " + root + "\nRun: aikito migrate workspace-resources"
		if err == nil || err.Error() != want {
			t.Fatalf("got %v, want %q", err, want)
		}
	})

	t.Run("pending_layout_journal_checked_before_marker", func(t *testing.T) {
		// Mid-migration the marker may not exist yet; the incomplete-migration
		// message must win over "needs migration", as in Python.
		root := t.TempDir()
		writePendingJournal(t, root, `{"changes":[{"kind":"layout"}]}`)
		err := RequireCurrentLayout(root)
		if err == nil || !strings.HasPrefix(err.Error(), "Workspace migration is incomplete: ") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("non_layout_pending_journal_does_not_block", func(t *testing.T) {
		root := currentLayoutRoot(t)
		writePendingJournal(t, root, `{"changes":[{"kind":"memory"}]}`)
		if err := RequireCurrentLayout(root); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("corrupted_journal_is_unsafe_state", func(t *testing.T) {
		root := currentLayoutRoot(t)
		path := writePendingJournal(t, root, `garbage`)
		err := RequireCurrentLayout(root)
		want := "Unsafe workspace transaction state: Invalid workspace journal: " + path
		if err == nil || err.Error() != want {
			t.Fatalf("got %v, want %q", err, want)
		}
	})

	t.Run("legacy_files_need_migration", func(t *testing.T) {
		root := currentLayoutRoot(t)
		if err := os.WriteFile(filepath.Join(root, "agents.toml"), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		err := RequireCurrentLayout(root)
		want := "Workspace needs migration: " + root +
			"\nRun: aikito migrate workspace-resources --dry-run\nThen: aikito migrate workspace-resources"
		if err == nil || err.Error() != want {
			t.Fatalf("got %v, want %q", err, want)
		}
	})

	t.Run("missing_marker_needs_migration", func(t *testing.T) {
		root := currentLayoutRoot(t)
		if err := os.Remove(filepath.Join(root, LayoutFile)); err != nil {
			t.Fatal(err)
		}
		err := RequireCurrentLayout(root)
		if err == nil || !strings.HasPrefix(err.Error(), "Workspace needs migration: "+root+"\nRun: ") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("unsupported_version", func(t *testing.T) {
		root := currentLayoutRoot(t)
		if err := os.WriteFile(filepath.Join(root, LayoutFile), []byte("version = 3\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := RequireCurrentLayout(root)
		if err == nil || err.Error() != "Unsupported workspace layout version 3: "+root {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("missing_resource_dir", func(t *testing.T) {
		root := currentLayoutRoot(t)
		if err := os.Remove(filepath.Join(root, "subagents")); err != nil {
			t.Fatal(err)
		}
		err := RequireCurrentLayout(root)
		if err == nil || err.Error() != "Workspace resource directory missing or unsafe: "+filepath.Join(root, "subagents") {
			t.Fatalf("got %v", err)
		}
	})
}
