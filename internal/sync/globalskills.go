package sync

import (
	"os"
	"path/filepath"

	"github.com/mr-miles/aikito-go/internal/workspace"
)

// LoadSelectedGlobalSkills mirrors global_skills.py's
// load_global_skills_list: the selected skill names from workspace
// skills.toml. A missing file, or any malformed shape, tolerantly yields an
// empty list rather than an error (matching Python's broad except).
func LoadSelectedGlobalSkills(aikitoDir string) []string {
	data, err := os.ReadFile(filepath.Join(aikitoDir, "skills.toml"))
	if err != nil {
		return nil
	}
	doc, err := workspace.DecodeTOML(data)
	if err != nil || doc == nil {
		return nil
	}
	raw, ok := doc["skills"].([]any)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			names = append(names, s)
		} else {
			return nil // non-string entry: Python's broad except discards the whole list
		}
	}
	return names
}
