//go:build e2e

package e2e

import "testing"

// TestE2EProjectSync replays each project-sync scenario against the Go
// binary and requires the transcript (every command's stdout, stderr and
// exit status) and the resulting home tree to match Python's exactly.
func TestE2EProjectSync(t *testing.T) {
	goCLI := func(t *testing.T, home, dir string, args ...string) runResult {
		t.Helper()
		return runBinaryIn(t, binPath, home, dir, nil, args...)
	}
	for _, sc := range projectSyncScenarios {
		t.Run(sc.name, func(t *testing.T) {
			live := runProjectSyncScenario(t, sc, goCLI)
			golden := loadGolden(t, "project_sync_"+sc.name)
			compareManifests(t, "project sync "+sc.name, live, golden)
		})
	}
}
