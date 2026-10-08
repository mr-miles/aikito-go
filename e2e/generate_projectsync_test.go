//go:build e2e_generate

package e2e

import "testing"

// TestGenerateProjectSyncGoldens captures every project-sync scenario from
// the reference Python CLI into e2e/testdata/project_sync_<name>/.
//
//	go test -tags e2e_generate -run TestGenerateProjectSyncGoldens ./e2e/... -v
func TestGenerateProjectSyncGoldens(t *testing.T) {
	pythonSrc := requirePython(t)
	py := func(t *testing.T, home, dir string, args ...string) runResult {
		t.Helper()
		return runBinaryIn(t, "python3", home, dir, []string{"PYTHONPATH=" + pythonSrc},
			append([]string{"-m", "aikito"}, args...)...)
	}
	for _, sc := range projectSyncScenarios {
		t.Run(sc.name, func(t *testing.T) {
			saveGolden(t, "project_sync_"+sc.name, runProjectSyncScenario(t, sc, py))
		})
	}
}
