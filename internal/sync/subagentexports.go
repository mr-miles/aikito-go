package sync

import "github.com/mr-miles/aikito-go/internal/subagent"

// LoadSubagentDefinitions is subagent.py's load_subagent_definitions with
// allow_empty=True, for read-only checks (doctor).
func LoadSubagentDefinitions(aikitoDir string) (map[string]subagent.Definition, error) {
	return loadSubagentDefinitions(aikitoDir)
}
