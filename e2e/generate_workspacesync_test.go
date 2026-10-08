//go:build e2e_generate

package e2e

import "testing"

// TestGenerateWorkspaceSyncGoldens captures every whole-workspace sync
// scenario from the reference Python CLI into
// e2e/testdata/workspace_sync_<name>/.
//
//	go test -tags e2e_generate -run TestGenerateWorkspaceSyncGoldens ./e2e/... -v
func TestGenerateWorkspaceSyncGoldens(t *testing.T) {
	pythonSrc := requirePython(t)
	py := func(t *testing.T, home, dir string, args ...string) runResult {
		t.Helper()
		return runBinaryIn(t, "python3", home, dir, []string{"PYTHONPATH=" + pythonSrc},
			append([]string{"-m", "aikito"}, args...)...)
	}
	for _, sc := range workspaceSyncScenarios {
		t.Run(sc.name, func(t *testing.T) {
			saveGolden(t, "workspace_sync_"+sc.name, runProjectSyncScenario(t, sc, py))
		})
	}
}
