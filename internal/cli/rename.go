package cli

import (
	"fmt"
	"io"
	"strings"
)

// cmdRename dispatches `aikito rename <kind> ...`. Only "memory" is ported
// (the only rename target cli_parser.py actually defines).
func cmdRename(args []string, stdout, stderr io.Writer, env Environment) int {
	if len(args) == 0 {
		return argparseRequired(stderr, "rename", "rename_target")
	}
	if args[0] != "memory" {
		return argparseSubError(stderr, "rename", fmt.Sprintf(
			"argument rename_target: invalid choice: '%s' (choose from memory)", args[0]))
	}
	return cmdRenameMemory(args[1:], stdout, stderr, env)
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
