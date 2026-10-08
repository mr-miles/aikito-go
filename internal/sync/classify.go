package sync

import (
	"path/filepath"
	"strings"

	"github.com/mr-miles/aikito-go/internal/workspace"
)

// Classifier maps a relative workspace resource path to its logical kind,
// mirroring resources.py's resource_kind_for_path. It returns ("", false)
// when the path doesn't match any known resource shape.
type Classifier func(path string, inboxPrefix string) (kind string, ok bool)

// DefaultClassifier is a faithful port of resource_kind_for_path, built only
// from internal/workspace's already-exported name-validation helpers
// (ValidateResourceName, ValidateMemoryName, ValidateProjectName,
// SubagentNamePattern, IsBundledSkillName) — no dependency on the (still
// separately evolving) full resource-scanner package. Cross-validated
// against the live Python function across 23 representative paths.
func DefaultClassifier(path string, inboxPrefix string) (string, bool) {
	parts := strings.Split(path, "/")

	if len(parts) == 1 && parts[0] == "skills.toml" {
		return "skills-config", true
	}
	if len(parts) == 1 && parts[0] == "config.toml" {
		return "workspace-config", true
	}
	if len(parts) == 2 {
		area, filename := parts[0], parts[1]
		stem := strings.TrimSuffix(filename, filepath.Ext(filename))
		switch {
		case area == "memory" && strings.HasSuffix(filename, ".md"):
			return "memory", true
		case area == "skills" && workspace.ValidateResourceName(filename, "skill") == "":
			if workspace.IsBundledSkillName(filename) {
				return "", false
			}
			return "skill", true
		case area == "agents" && strings.HasSuffix(filename, ".toml") && workspace.ValidateResourceName(stem, "agent") == "":
			return "agent", true
		case area == "subagents" && strings.HasSuffix(filename, ".md") && workspace.SubagentNamePattern.MatchString(stem):
			return "subagent", true
		case area == "mcps" && strings.HasSuffix(filename, ".toml") && workspace.ValidateResourceName(stem, "mcp") == "":
			return "mcp", true
		}
		if path == "global/AGENTS.md" {
			return "global-instructions", true
		}
	}
	if len(parts) == 3 && parts[0] == "memory" && parts[1] == "notes" {
		if strings.HasSuffix(parts[2], ".md") {
			noteStem := strings.TrimSuffix(parts[2], ".md")
			if workspace.ValidateMemoryName(noteStem) == "" {
				return "memory", true
			}
		}
		return "", false
	}
	if len(parts) == 3 && parts[0] == "projects" && workspace.ValidateProjectName(parts[1]) == "" {
		if parts[2] == "agent.toml" {
			return "project-config", true
		}
		if parts[2] == "AGENTS.md" {
			return "project-instructions", true
		}
	}
	if (len(parts) == 4 || len(parts) == 5) && parts[0] == "projects" &&
		workspace.ValidateProjectName(parts[1]) == "" && parts[2] == "memory" {
		if len(parts) == 4 && strings.HasSuffix(parts[3], ".md") {
			return "memory", true
		}
		if len(parts) == 5 && parts[3] == "notes" && strings.HasSuffix(parts[4], ".md") {
			noteStem := strings.TrimSuffix(parts[4], ".md")
			if workspace.ValidateMemoryName(noteStem) == "" {
				return "memory", true
			}
		}
	}
	if inboxPrefix != "" {
		prefixParts := strings.Split(inboxPrefix, "/")
		if len(parts) > len(prefixParts) {
			match := true
			for i, p := range prefixParts {
				if parts[i] != p {
					match = false
					break
				}
			}
			if match && strings.HasSuffix(parts[len(parts)-1], ".md") {
				return "inbox", true
			}
		}
	}
	return "", false
}
