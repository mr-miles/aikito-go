package linkplan

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/mr-miles/aikito-go/internal/compat"
	"github.com/mr-miles/aikito-go/internal/registry"
)

// PathlibJoin is `Path(dir) / rel`, which keeps ".." components (see
// pathlibJoin).
func PathlibJoin(dir, rel string) string { return pathlibJoin(dir, rel) }

// Dest is `raw_link_target or resolved_link_target or "unknown"`, the
// destination plan messages show for an observed symlink.
func (o ObservedLink) Dest() string { return o.dest() }

func lexists(p string) bool { return isSymlink(p) || exists(p) }

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// CanonicalNonEmpty is `path.read_text(errors="replace").strip() != ""`.
func CanonicalNonEmpty(p string) (bool, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(compat.DecodeUTF8Replace(data)) != "", nil
}

// BuildProjectInstructionBatch ports instructions.py's
// build_project_instruction_batch: the project-instruction targets of every
// active and offline checkout, merged by physical location, plus legacy
// <checkout>/.agents/AGENTS.md entries to clean up.
func BuildProjectInstructionBatch(workspaceRoot, projectName string, active []string, home string, reg *registry.AgentRegistry, offline []string) (InstructionBatch, error) {
	if reg == nil {
		reg = registry.Load(workspaceRoot, home)
	}
	canonical := filepath.Join(workspaceRoot, "projects", projectName, "AGENTS.md")
	var all, stale []registry.Target
	addTarget := func(t registry.Target) {
		for i, existing := range all {
			if !IsSameTargetLocation(t.Path, existing.Path) {
				continue
			}
			merged := existing
			merged.Consumers = uniqueStrings(append(append([]string(nil), existing.Consumers...), t.Consumers...))
			merged.ConsumerDisplayNames = uniqueStrings(append(append([]string(nil), existing.ConsumerDisplayNames...), t.ConsumerDisplayNames...))
			// Python's merged Target drops consumer_agents, so availability
			// falls back to bundled_agent(name) for each consumer.
			merged.ConsumerAgents = nil
			for _, n := range merged.Consumers {
				if a, err := registry.BundledAgent(n, home); err == nil {
					merged.ConsumerAgents = append(merged.ConsumerAgents, a)
				} else if a, ok := reg.Get(n); ok {
					merged.ConsumerAgents = append(merged.ConsumerAgents, a)
				}
			}
			all[i] = merged
			return
		}
		all = append(all, t)
	}
	resolve := func(co string) error {
		targets, err := registry.ResolveTargets("project_instructions", workspaceRoot, home, reg,
			registry.ResolveTargetsOptions{ProjectPath: co, ProjectName: projectName})
		for _, t := range targets {
			addTarget(t)
		}
		return err
	}
	for _, co := range active {
		if err := resolve(co); err != nil {
			return InstructionBatch{}, err
		}
		legacy := filepath.Join(co, ".agents", "AGENTS.md")
		formal := false
		for _, t := range all {
			if IsSameTargetLocation(t.Path, legacy) {
				formal = true
			}
		}
		if formal || !lexists(legacy) {
			continue
		}
		dup := false
		for _, s := range stale {
			if IsSameTargetLocation(legacy, s.Path) {
				dup = true
			}
		}
		if !dup {
			stale = append(stale, registry.Target{Kind: "instruction_link", Scope: "project", Path: legacy,
				CanonicalSource: canonical, ConsumerDisplayNames: []string{"Legacy .agents"}})
		}
	}
	for _, co := range offline {
		if err := resolve(co); err != nil {
			return InstructionBatch{}, err
		}
	}
	enabled := false
	if isFile(canonical) {
		enabled, _ = CanonicalNonEmpty(canonical)
	}
	first := ""
	if len(active) > 0 {
		first = active[0]
	} else if len(offline) > 0 {
		first = offline[0]
	}
	return InstructionBatch{Scope: "project", CanonicalSource: canonical, Targets: all, Enabled: enabled,
		StaleTargets: stale, Checkout: first, ProjectName: projectName, OfflineCheckouts: offline}, nil
}
