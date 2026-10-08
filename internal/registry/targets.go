package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-go/internal/compat"
)

// Target ports agents.py's Target: a physical destination for a resource,
// with the agents that consume it. Several agents configured with the same
// physical path share one Target.
//
// Kinds: "managed_entry", "managed_container", "consumer_link".
type Target struct {
	Kind                 string
	Scope                string // "global" or "project"
	Path                 string
	CanonicalSource      string // "" when there is none (Python's None)
	Consumers            []string
	ConsumerDisplayNames []string
	ConsumerAgents       []Agent
}

// IsSameObject reports whether Path and CanonicalSource are the same
// filesystem object.
func (t Target) IsSameObject() bool {
	if t.CanonicalSource == "" {
		return false
	}
	return normalizedPhysicalPath(t.Path) == normalizedPhysicalPath(t.CanonicalSource)
}

func existingProbe(p string) (string, bool) {
	for {
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p, false
		}
		p = parent
	}
}

func normalizedPhysicalPath(path string) string {
	physical := compat.PhysicalPath(path)
	probe := physical
	if st, err := os.Stat(physical); err != nil || !st.IsDir() {
		probe = filepath.Dir(physical)
	}
	if p, ok := existingProbe(probe); ok && compat.DirectoryFoldsCase(p) {
		return strings.ToLower(physical)
	}
	return physical
}

// physicalTargetKey identifies a directory entry without following its
// final symlink.
func physicalTargetKey(path string) string {
	parent := compat.PhysicalPath(filepath.Dir(path))
	name := filepath.Base(path)
	if p, ok := existingProbe(parent); ok && compat.DirectoryFoldsCase(p) {
		return strings.ToLower(parent) + "/" + strings.ToLower(name)
	}
	return parent + "/" + name
}

type agentPath struct {
	path  string
	agent Agent
}

type pathGroup struct {
	path   string
	agents []Agent
}

func groupAgentPaths(entries []agentPath) []pathGroup {
	index := map[string]int{}
	var groups []pathGroup
	for _, e := range entries {
		key := physicalTargetKey(e.path)
		i, ok := index[key]
		if !ok {
			i = len(groups)
			index[key] = i
			groups = append(groups, pathGroup{path: e.path})
		}
		groups[i].agents = append(groups[i].agents, e.agent)
	}
	sort.SliceStable(groups, func(a, b int) bool { return groups[a].path < groups[b].path })
	return groups
}

// CheckTargetAvailability combines the availability of a target's consumers.
func CheckTargetAvailability(t Target, home string) AgentAvailability {
	var statuses []AgentAvailability
	path := t.Path
	for _, a := range t.ConsumerAgents {
		statuses = append(statuses, CheckAgentAvailabilityForAgent(a, home, &path))
	}
	for _, s := range statuses {
		if s.IsInstalled() {
			return AgentAvailability{Status: "installed", Evidence: s.Evidence}
		}
	}
	if len(statuses) > 0 {
		all := true
		for _, s := range statuses {
			if !s.IsNotInstalled() {
				all = false
			}
		}
		if all {
			return AgentAvailability{Status: "not_installed", Evidence: "all_consumers_not_installed"}
		}
	}
	return AgentAvailability{Status: "unknown", Evidence: "cannot_determine"}
}

// ResolveTargetsOptions are resolve_targets' keyword arguments.
type ResolveTargetsOptions struct {
	ProjectPath string
	ProjectName string
	ActiveOnly  bool
}

func parentExists(p string) bool {
	_, err := os.Stat(filepath.Dir(p))
	return err == nil
}

// ResolveTargets ports agents.py resolve_targets for "global_skills",
// "global_instructions" and "project_instructions".
func ResolveTargets(kind, aikitoDir, home string, reg *AgentRegistry, opts ResolveTargetsOptions) ([]Target, error) {
	var entries []agentPath
	var canonical, scope string
	switch kind {
	case "global_skills":
		scope, canonical = "global", filepath.Join(home, ".agents", "skills")
		for _, a := range reg.Values() {
			if a.SkillsPath != nil {
				entries = append(entries, agentPath{*a.SkillsPath, a})
			}
		}
	case "global_instructions":
		scope, canonical = "global", filepath.Join(aikitoDir, "global", "AGENTS.md")
		for _, a := range reg.Values() {
			if a.InstructionPath != nil {
				entries = append(entries, agentPath{*a.InstructionPath, a})
			}
		}
	case "project_instructions":
		if opts.ProjectPath == "" {
			return nil, nil
		}
		scope = "project"
		if opts.ProjectName != "" {
			canonical = filepath.Join(aikitoDir, "projects", opts.ProjectName, "AGENTS.md")
		}
		for _, a := range reg.Values() {
			if a.ProjectInstructionPath != nil {
				entries = append(entries, agentPath{filepath.Join(opts.ProjectPath, *a.ProjectInstructionPath), a})
			}
		}
	default:
		return nil, fmt.Errorf("Unknown resource kind for target resolution: %s", kind)
	}

	var targets []Target
	for _, g := range groupAgentPaths(entries) {
		t := Target{Kind: "consumer_link", Scope: scope, Path: g.path, CanonicalSource: canonical, ConsumerAgents: g.agents}
		for _, a := range g.agents {
			t.Consumers = append(t.Consumers, a.Name)
			t.ConsumerDisplayNames = append(t.ConsumerDisplayNames, a.DisplayName)
		}
		if opts.ActiveOnly {
			if kind == "project_instructions" {
				if !parentExists(g.path) && !CheckTargetAvailability(t, home).IsInstalled() {
					continue
				}
			} else if !CheckTargetAvailability(t, home).IsInstalled() && !parentExists(g.path) {
				continue
			}
		}
		targets = append(targets, t)
	}
	return targets, nil
}
