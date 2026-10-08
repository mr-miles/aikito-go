package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-go/internal/workspace"
)

// readTextIgnore is Path.read_text(errors="ignore"): invalid UTF-8 is
// dropped and universal newlines turn \r\n and \r into \n.
func readTextIgnore(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.ToValidUTF8(string(data), "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n"), nil
}

// memoryScopeNotes ports memory.py resolve_memory_note_scope: the notes
// directory a note's wikilinks are resolved within (the note's own scope).
func memoryScopeNotes(notePath, operation string) (string, error) {
	if filepath.Base(filepath.Dir(notePath)) != "notes" {
		return "", fmt.Errorf("Cannot %s '%s'. Only atomic memory notes in 'notes/' can be changed.", operation, filepath.Base(notePath))
	}
	return filepath.Dir(notePath), nil
}

// scopeNoteFiles is sorted(scope_notes_dir.glob("*.md")), regular files only
// (Python skips anything it can't read as text).
func scopeNoteFiles(notesDir string) []string {
	var out []string
	for _, p := range notesGlob(notesDir) {
		if isRegularFilePath(p) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func relToWorkspace(aikitoDir, p string) string {
	if rel, err := filepath.Rel(aikitoDir, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return p
}

// cmdRenameMemory ports cli.py cmd_rename_memory and memory.py
// rename_memory_note: rename the note file, then rewrite [[old]] wikilinks
// (followed by |, # or ]) in every note of the same scope.
func cmdRenameMemory(args []string, stdout, stderr io.Writer, env Environment) int {
	parsed, ok := parseArgparseOpts("rename memory", args, nil, nil, nil, 2, stderr)
	if !ok {
		return 2
	}
	if missing := []string{"target", "new_name"}[len(parsed.positionals):]; len(missing) > 0 {
		return argparseRequired(stderr, "rename memory", missing...)
	}
	target, newName := parsed.positionals[0], parsed.positionals[1]

	if msg := workspace.ValidateMemoryName(newName); msg != "" {
		fmt.Fprintf(stderr, "[ERROR] %s\n", msg)
		return 1
	}
	aikitoDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if err := requireLayoutLikePython(aikitoDir); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	targetPath, ok := resolveMemoryTarget(aikitoDir, target, "rename", "", stderr)
	if !ok {
		return 1
	}

	oldStem := strings.TrimSuffix(filepath.Base(targetPath), ".md")
	if oldStem == newName {
		fmt.Fprintf(stderr, "[ERROR] Note '%s' is already named '%s'.\n", oldStem, newName)
		return 1
	}
	newPath := filepath.Join(filepath.Dir(targetPath), newName+".md")
	if _, err := os.Stat(newPath); err == nil {
		a, _ := workspace.ResolvePath(newPath)
		b, _ := workspace.ResolvePath(targetPath)
		if a != b {
			fmt.Fprintf(stderr, "[ERROR] Target memory note '%s' already exists.\n", filepath.Base(newPath))
			return 1
		}
	}
	notesDir, err := memoryScopeNotes(targetPath, "rename")
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if err := os.Rename(targetPath, newPath); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	var refactored []string
	for _, note := range scopeNoteFiles(notesDir) {
		content, err := readTextIgnore(note)
		if err != nil {
			continue
		}
		updated, n := rewriteWikilinks(content, oldStem, newName)
		if n > 0 {
			if err := os.WriteFile(note, []byte(updated), 0o644); err == nil {
				refactored = append(refactored, note)
			}
		}
	}

	fmt.Fprintf(stdout, "[OK] Renamed memory note '%s' → '%s'\n", oldStem, newName)
	fmt.Fprintf(stdout, "  - File: %s\n", relToWorkspace(aikitoDir, newPath))
	if len(refactored) > 0 {
		fmt.Fprintf(stdout, "  - Updated inbound wikilinks in %d file(s):\n", len(refactored))
		for _, p := range refactored {
			fmt.Fprintf(stdout, "    * %s\n", relToWorkspace(aikitoDir, p))
		}
	} else {
		fmt.Fprintln(stdout, "  - No inbound wikilinks found in other notes.")
	}
	return 0
}

// cmdRmMemory ports cli.py cmd_rm_memory and memory.py remove_memory_note:
// report inbound wikilinks from the note's scope, then delete the note.
func cmdRmMemory(verb string, args []string, stdout, stderr io.Writer, env Environment) int {
	cmdPath := verb + " memory"
	parsed, ok := parseArgparseOpts(cmdPath, args, nil, nil, nil, 1, stderr)
	if !ok {
		return 2
	}
	if len(parsed.positionals) == 0 {
		return argparseRequired(stderr, cmdPath, "target")
	}
	aikitoDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if err := requireLayoutLikePython(aikitoDir); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	// Python names the operation "rm" here whichever spelling was typed.
	targetPath, ok := resolveMemoryTarget(aikitoDir, parsed.positionals[0], "rm", "", stderr)
	if !ok {
		return 1
	}
	stem := strings.TrimSuffix(filepath.Base(targetPath), ".md")
	notesDir, err := memoryScopeNotes(targetPath, "remove")
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	type ref struct {
		note    string
		line    int
		content string
	}
	var refs []ref
	resolvedTarget, _ := workspace.ResolvePath(targetPath)
	for _, note := range scopeNoteFiles(notesDir) {
		if r, _ := workspace.ResolvePath(note); r == resolvedTarget {
			continue
		}
		content, err := readTextIgnore(note)
		if err != nil {
			continue
		}
		for i, line := range pythonSplitLines(content) {
			line = strings.TrimRight(line, "\r\n")
			if _, n := rewriteWikilinks(line, stem, stem); n > 0 {
				refs = append(refs, ref{note, i + 1, workspace.PyStrip(line)})
			}
		}
	}

	if err := os.Remove(targetPath); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "[OK] Removed memory note '%s' (%s)\n", stem, filepath.Base(targetPath))
	if len(refs) > 0 {
		fmt.Fprintf(stdout, "  - [WARN] %d inbound reference(s) still exist:\n", len(refs))
		for _, r := range refs {
			fmt.Fprintf(stdout, "    * %s:%d: %s\n", relToWorkspace(aikitoDir, r.note), r.line, r.content)
		}
		fmt.Fprintln(stdout, "    Please review and update the referencing notes if necessary.")
	} else {
		fmt.Fprintln(stdout, "  - No inbound references found.")
	}
	return 0
}

// cmdRmInbox ports cli.py cmd_rm_inbox and inbox.py remove_inbox_note.
func cmdRmInbox(verb string, args []string, stdout, stderr io.Writer, env Environment) int {
	cmdPath := verb + " inbox"
	parsed, ok := parseArgparseOpts(cmdPath, args, nil, nil, nil, 1, stderr)
	if !ok {
		return 2
	}
	if len(parsed.positionals) == 0 {
		return argparseRequired(stderr, cmdPath, "target")
	}
	aikitoDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if err := requireLayoutLikePython(aikitoDir); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	dir := workspace.GetInboxPath(aikitoDir, env.Home)
	targetPath, ok := resolveInboxTarget(dir, parsed.positionals[0], verb, stderr)
	if !ok {
		return 1
	}
	ident := strings.TrimSuffix(filepath.Base(targetPath), ".md")
	if rel, err := filepath.Rel(dir, targetPath); err == nil && !strings.HasPrefix(rel, "..") {
		ident = strings.TrimSuffix(filepath.ToSlash(rel), ".md")
	}
	if err := os.Remove(targetPath); err != nil {
		fmt.Fprintf(stderr, "[ERROR] Failed to remove inbox note '%s': %v\n", ident, err)
		return 1
	}
	fmt.Fprintf(stdout, "[OK] Removed inbox note '%s' (%s)\n", ident, filepath.Base(targetPath))
	return 0
}
