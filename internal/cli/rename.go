package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-rs/internal/workspace"
)

// cmdRename dispatches `aikito rename <kind> ...`. Only "memory" is ported
// (the only rename target cli_parser.py actually defines).
func cmdRename(args []string, stdout, stderr io.Writer, env Environment) int {
	if len(args) == 0 || args[0] != "memory" {
		fmt.Fprintln(stderr, "[ERROR] Usage: aikito rename memory <target> <new_name>")
		return 2
	}
	return cmdRenameMemory(args[1:], stdout, stderr, env)
}

// cmdRenameMemory ports memory.py's rename_memory_note: renames a memory
// note's file and rewrites inbound [[wikilink]] references to it within
// notes in the same scope (global vs. a specific project), matching
// Python's lookahead-based pattern \[\[old_stem(?=[|#\]]) — replaced with a
// hand-rolled scan here since Go's RE2 regexp engine has no lookahead
// support at all.
func cmdRenameMemory(args []string, stdout, stderr io.Writer, env Environment) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "[ERROR] Usage: aikito rename memory <target> <new_name>")
		return 2
	}
	target, newName := args[0], args[1]

	if msg := workspace.ValidateMemoryName(newName); msg != "" {
		fmt.Fprintf(stderr, "[ERROR] %s\n", msg)
		return 1
	}

	aikitoDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if msg := checkWorkspaceInitialized(aikitoDir); msg != "" {
		fmt.Fprintf(stderr, "[ERROR] %s\n", msg)
		return 1
	}

	// Same notes/-only, stem-keyed resolution as rm.go's cmdRmMemory and
	// the fixed show.go's cmdShowMemory (global scope only — rename memory
	// has no --project flag in cli_parser.py, unlike show/maintain).
	notesDir := filepath.Join(aikitoDir, "memory", "notes")
	var names []string
	byStem := map[string]string{}
	entries, _ := os.ReadDir(notesDir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		stem := strings.TrimSuffix(e.Name(), ".md")
		names = append(names, stem)
		byStem[stem] = filepath.Join(notesDir, e.Name())
	}
	sort.Strings(names)

	matched, ok := resolveByName(names, target, "rename", resolveLabels{
		conflictNoun: "memory notes", specifyLine: "Please specify the full identifier, e.g.:",
		cmdName: "memory", notFoundSingular: "Memory note",
		notFoundHint: "Run 'aikito show memory' to view available notes.",
	}, stderr)
	if !ok {
		return 1
	}

	oldStem := matched
	oldPath := byStem[oldStem]
	if oldStem == newName {
		fmt.Fprintf(stderr, "[ERROR] Note '%s' is already named '%s'.\n", oldStem, newName)
		return 1
	}
	newPath := filepath.Join(notesDir, newName+".md")
	if newInfo, err := os.Stat(newPath); err == nil {
		if oldInfo, err2 := os.Stat(oldPath); err2 != nil || !os.SameFile(newInfo, oldInfo) {
			fmt.Fprintf(stderr, "[ERROR] Target memory note '%s.md' already exists.\n", newName)
			return 1
		}
	}

	if err := os.Rename(oldPath, newPath); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	var refactored []string
	noteEntries, _ := os.ReadDir(notesDir)
	sort.Slice(noteEntries, func(i, j int) bool { return noteEntries[i].Name() < noteEntries[j].Name() })
	for _, e := range noteEntries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		notePath := filepath.Join(notesDir, e.Name())
		data, rerr := os.ReadFile(notePath)
		if rerr != nil {
			continue
		}
		newContent, n := rewriteWikilinks(string(data), oldStem, newName)
		if n > 0 {
			if werr := os.WriteFile(notePath, []byte(newContent), 0o644); werr == nil {
				refactored = append(refactored, filepath.Join("memory", "notes", e.Name()))
			}
		}
	}

	fmt.Fprintf(stdout, "[OK] Renamed memory note '%s' → '%s'\n", oldStem, newName)
	fmt.Fprintf(stdout, "  - File: %s\n", filepath.Join("memory", "notes", newName+".md"))
	if len(refactored) > 0 {
		fmt.Fprintf(stdout, "  - Updated inbound wikilinks in %d file(s):\n", len(refactored))
		for _, p := range refactored {
			fmt.Fprintf(stdout, "    * %s\n", p)
		}
	} else {
		fmt.Fprintln(stdout, "  - No inbound wikilinks found in other notes.")
	}
	return 0
}

// rewriteWikilinks replaces every occurrence of "[[oldStem" in content that
// is immediately followed by '|', '#', or ']' (a complete wikilink target,
// not a longer name sharing oldStem as a prefix) with "[[newName", leaving
// everything after that point (the alias/heading/closing brackets)
// untouched. Mirrors Python's re.compile(r"\[\[" + re.escape(old_stem) +
// r"(?=[|#\]])").subn(f"[[{new_name}", content).
func rewriteWikilinks(content, oldStem, newName string) (string, int) {
	needle := "[[" + oldStem
	var b strings.Builder
	count := 0
	i := 0
	for {
		idx := strings.Index(content[i:], needle)
		if idx < 0 {
			b.WriteString(content[i:])
			break
		}
		idx += i
		nextPos := idx + len(needle)
		if nextPos < len(content) {
			c := content[nextPos]
			if c == '|' || c == '#' || c == ']' {
				b.WriteString(content[i:idx])
				b.WriteString("[[")
				b.WriteString(newName)
				count++
				i = nextPos
				continue
			}
		}
		b.WriteString(content[i : idx+1])
		i = idx + 1
	}
	return b.String(), count
}
