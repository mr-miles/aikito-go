//go:build e2e

package e2e

import "testing"

// TestE2EWorkspaceSync replays each whole-workspace `aikito sync` scenario
// against the Go binary; transcript and home tree must match Python's.
func TestE2EWorkspaceSync(t *testing.T) {
	goCLI := func(t *testing.T, home, dir string, args ...string) runResult {
		t.Helper()
		return runBinaryIn(t, binPath, home, dir, nil, args...)
	}
	for _, sc := range workspaceSyncScenarios {
		t.Run(sc.name, func(t *testing.T) {
			live := runProjectSyncScenario(t, sc, goCLI)
			compareManifests(t, "workspace sync "+sc.name, live, loadGolden(t, "workspace_sync_"+sc.name))
		})
	}
}
