package sync

import (
	"path/filepath"

	"github.com/mr-miles/aikito-rs/internal/registry"
)

// GlobalInstructionPlanItem is one planned (agent) symlink for global
// instructions.
type GlobalInstructionPlanItem struct {
	Op LinkOperation
}

// BuildGlobalInstructionsPlan plans a LinkOperation for every configured
// agent's InstructionPath, each pointing at the single canonical
// <aikitoDir>/global/AGENTS.md. Unlike skills, no two built-in agents share
// an instruction path, so there is no shared-physical-target case to
// consider here (see link.go's package doc for the general simplification
// versus Python's full Target/resolve_targets model).
func BuildGlobalInstructionsPlan(aikitoDir, home string, reg *registry.AgentRegistry, force bool) ([]GlobalInstructionPlanItem, error) {
	canonical := filepath.Join(aikitoDir, "global", "AGENTS.md")
	var items []GlobalInstructionPlanItem
	for _, agent := range reg.Values() {
		if agent.InstructionPath == nil {
			continue
		}
		op, err := PlanSymlink(*agent.InstructionPath, canonical, agent.Name, "", force)
		if err != nil {
			return nil, err
		}
		items = append(items, GlobalInstructionPlanItem{Op: op})
	}
	return items, nil
}
