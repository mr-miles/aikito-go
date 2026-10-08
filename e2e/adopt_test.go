//go:build e2e

package e2e

import (
	"os"
	"reflect"
	"testing"

	"github.com/mr-miles/aikito-go/internal/workspace"
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
	home := initWorkspace(t)

	existingConfig := `{"mcpServers": {"existing-server": {"type": "http", "url": "https://existing.example.com/mcp"}}}`
	if err := os.WriteFile(home+"/.claude.json", []byte(existingConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runGo(t, home, "adopt")
	if res.ExitCode != 0 {
		t.Fatalf("go adopt failed (exit %d): %s\n%s", res.ExitCode, res.Stdout, res.Stderr)
	}

	goData, err := os.ReadFile(home + "/aikito/mcps/existing-server.toml")
	if err != nil {
		t.Fatalf("go: expected mcps/existing-server.toml to be adopted: %v", err)
	}
	goDoc, err := workspace.DecodeTOML(goData)
	if err != nil {
		t.Fatalf("decoding go's adopted TOML: %v\n%s", err, goData)
	}

	pyData := loadGoldenSingleFile(t, "adopt_mcp", "adopted.toml")
	pyDoc, err := workspace.DecodeTOML(pyData)
	if err != nil {
		t.Fatalf("decoding golden (python) adopted TOML: %v\n%s", err, pyData)
	}
	if !reflect.DeepEqual(goDoc, pyDoc) {
		t.Errorf("adopted MCP server fields differ:\n--- go ---\n%#v\n--- golden (python) ---\n%#v", goDoc, pyDoc)
	}

	// Re-running adopt on an already-adopted, untouched workspace must be a
	// clean no-op (this is a self-referential property of the Go tool, not
	// something that needs a Python-derived golden).
	res2 := runGo(t, home, "adopt")
	if res2.ExitCode != 0 {
		t.Errorf("go: re-running adopt should be a clean no-op, got exit %d: %s", res2.ExitCode, res2.Stdout)
	}
}
