package sync

import (
	"os"
	"path/filepath"

	"github.com/mr-miles/aikito-rs/internal/registry"
	"github.com/mr-miles/aikito-rs/internal/workspace"
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

// GlobalSkillPlanItem is one planned (agent, skill) symlink.
type GlobalSkillPlanItem struct {
	Skill string
	Op    LinkOperation
}

// BuildGlobalSkillsPlan plans a LinkOperation for every (selected skill,
// agent with a configured SkillsPath) pair. See link.go's package doc for
// the deliberate simplification versus Python's shared-container model.
func BuildGlobalSkillsPlan(aikitoDir, home string, reg *registry.AgentRegistry, force bool) ([]GlobalSkillPlanItem, error) {
	skills := LoadSelectedGlobalSkills(aikitoDir)
	var items []GlobalSkillPlanItem
	for _, agent := range reg.Values() {
		if agent.SkillsPath == nil {
			continue
		}
		for _, skill := range skills {
			target := filepath.Join(*agent.SkillsPath, skill)
			canonical := filepath.Join(aikitoDir, "skills", skill)
			op, err := PlanSymlink(target, canonical, agent.Name, skill, force)
			if err != nil {
				return nil, err
			}
			items = append(items, GlobalSkillPlanItem{Skill: skill, Op: op})
		}
	}
	return items, nil
}
