package cli

import "testing"

// destructive_vectors.json is generated from the reference CLI by
// testdata/gen_destructive_vectors.py: rename memory (wikilink rewriting,
// conflicts, invalid names), rm memory/inbox (every target form), rm mcp
// (--sync, --force, drift) and doctor --fix's stale local-state cleanup,
// with the resulting home and workspace trees.
func TestDestructiveCommandsMatchPython(t *testing.T) {
	replayCLIVectors(t, "testdata/destructive_vectors.json")
}
