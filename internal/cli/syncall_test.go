package cli

import "testing"

// syncall_vectors.json is generated from the reference CLI by
// testdata/gen_syncall_vectors.py: the whole-workspace `aikito sync`
// (cmd_sync_all) across fresh, rich, conflicting and broken workspaces, its
// --dry-run/--verbose forms, and parent-flag handling for `aikito sync
// <target>`.
func TestSyncAllMatchesPython(t *testing.T) {
	replayCLIVectors(t, "testdata/syncall_vectors.json")
}
