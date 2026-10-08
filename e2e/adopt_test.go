//go:build e2e

package e2e

import (
	"os"
	"reflect"
	"testing"

	"github.com/mr-miles/aikito-rs/internal/workspace"
)

// TestE2EAdoptDiscoversExistingMCPServer confirms `aikito adopt` discovers
// a pre-existing agent-native MCP server entry and writes an equivalent
// canonical mcps/<name>.toml, matching Python field-for-field. File content
// is compared by decoded VALUE, not raw bytes: this Go port's TOML writer
// has an already-documented, separate key-ordering limitation (see
// internal/sync/resourcewrite.go's package doc — go-toml/v2 decodes into
// an unordered map, so exact key order isn't always reproduced), which is
// orthogonal to whether adopt's discovery/decision logic is correct.
func TestE2EAdoptDiscoversExistingMCPServer(t *testing.T) {
	goHome, pyHome, pythonSrc := initBothWorkspaces(t)

	existingConfig := `{"mcpServers": {"existing-server": {"type": "http", "url": "https://existing.example.com/mcp"}}}`
	if err := os.WriteFile(goHome+"/.claude.json", []byte(existingConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pyHome+"/.claude.json", []byte(existingConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	goRes := runGo(t, goHome, "adopt")
	if goRes.ExitCode != 0 {
		t.Fatalf("go adopt failed (exit %d): %s\n%s", goRes.ExitCode, goRes.Stdout, goRes.Stderr)
	}
	pyRes := runPython(t, pythonSrc, pyHome, "adopt")
	if pyRes.ExitCode != 0 {
		t.Fatalf("python adopt failed (exit %d): %s\n%s", pyRes.ExitCode, pyRes.Stdout, pyRes.Stderr)
	}

	goData, err := os.ReadFile(goHome + "/aikito/mcps/existing-server.toml")
	if err != nil {
		t.Fatalf("go: expected mcps/existing-server.toml to be adopted: %v", err)
	}
	pyData, err := os.ReadFile(pyHome + "/aikito/mcps/existing-server.toml")
	if err != nil {
		t.Fatalf("python: expected mcps/existing-server.toml to be adopted: %v", err)
	}

	goDoc, err := workspace.DecodeTOML(goData)
	if err != nil {
		t.Fatalf("decoding go's adopted TOML: %v\n%s", err, goData)
	}
	pyDoc, err := workspace.DecodeTOML(pyData)
	if err != nil {
		t.Fatalf("decoding python's adopted TOML: %v\n%s", err, pyData)
	}
	if !reflect.DeepEqual(goDoc, pyDoc) {
		t.Errorf("adopted MCP server fields differ:\n--- go ---\n%#v\n--- python ---\n%#v", goDoc, pyDoc)
	}

	// Re-running adopt on an already-adopted, untouched workspace must be a
	// clean no-op on both sides (second run's exit code + no new files).
	goRes2 := runGo(t, goHome, "adopt")
	pyRes2 := runPython(t, pythonSrc, pyHome, "adopt")
	if goRes2.ExitCode != 0 {
		t.Errorf("go: re-running adopt should be a clean no-op, got exit %d: %s", goRes2.ExitCode, goRes2.Stdout)
	}
	if pyRes2.ExitCode != 0 {
		t.Errorf("python: re-running adopt should be a clean no-op, got exit %d: %s", pyRes2.ExitCode, pyRes2.Stdout)
	}
}
