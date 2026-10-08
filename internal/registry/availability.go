package registry

import (
	"os"
	"os/exec"
	"path/filepath"
)

// CheckAgentAvailabilityForAgent mirrors check_agent_availability for an
// already-loaded Agent. Tri-state: never collapse to a bool — "unknown" is
// a distinct, common outcome for a custom/undeclared agent with no detect
// table and no resolvable target parent.
func CheckAgentAvailabilityForAgent(agent Agent, home string, targetPath *string) AgentAvailability {
	detection := agent.Detect
	if detection != nil && (len(detection.Commands) > 0 || len(detection.Paths) > 0) {
		for _, command := range detection.Commands {
			if _, err := exec.LookPath(command); err == nil {
				return AgentAvailability{Status: "installed", Evidence: "binary_on_path"}
			}
		}
		for _, p := range detection.Paths {
			if _, err := os.Stat(filepath.Join(home, filepath.FromSlash(p))); err == nil {
				return AgentAvailability{Status: "installed", Evidence: "marker_directory"}
			}
		}
		return AgentAvailability{Status: "not_installed", Evidence: "marker_not_found"}
	}

	var candParent *string
	if targetPath != nil {
		p := filepath.Dir(*targetPath)
		candParent = &p
	}
	if candParent == nil {
		if agent.SkillsPath != nil {
			p := filepath.Dir(*agent.SkillsPath)
			candParent = &p
		} else if agent.InstructionPath != nil {
			p := filepath.Dir(*agent.InstructionPath)
			candParent = &p
		}
	}
	if candParent != nil {
		if _, err := os.Stat(*candParent); err == nil {
			return AgentAvailability{Status: "installed", Evidence: "target_parent_exists"}
		}
	}
	return AgentAvailability{Status: "unknown", Evidence: "cannot_determine"}
}

// CheckAgentAvailabilityByName mirrors check_agent_availability's
// string-name convenience path: it resolves ONLY the bundled template for
// name (via BundledAgent), ignoring any workspace-declared override for a
// custom agent of the same name. This is a real, intentional Python
// behavior (not a Go-port gap) — callers that need workspace-aware
// detection for a custom agent must load an AgentRegistry/AgentDefinition
// and call CheckAgentAvailabilityForAgent directly instead of by name.
func CheckAgentAvailabilityByName(name, home string, targetPath *string) (AgentAvailability, error) {
	agent, err := BundledAgent(name, home)
	if err != nil {
		return AgentAvailability{}, err
	}
	return CheckAgentAvailabilityForAgent(agent, home, targetPath), nil
}

// IsAgentInstalled mirrors is_agent_installed: the canonical tri-state
// install signal collapsed to *bool (true/false/nil for unknown).
func IsAgentInstalled(agent Agent, home string, targetPath *string) *bool {
	avail := CheckAgentAvailabilityForAgent(agent, home, targetPath)
	if avail.IsInstalled() {
		t := true
		return &t
	}
	if avail.IsNotInstalled() {
		f := false
		return &f
	}
	return nil
}
