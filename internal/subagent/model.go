// Package subagent ports the canonical Subagent data model and the 8
// per-platform native rendering/validation adapters from
// aikito/src/aikito/{subagent.py (data model only),subagent_adapters.py,
// subagent_validation.py}.
//
// Deferred (out of scope here): subagent.py's SubagentPlan/build_subagent_plan
// and its execution path depend on config_runtime.py's generic
// ConfigTarget/ConfigOperation/FileMutationPlan precondition framework, which
// is not yet ported to Go. This package only covers the per-platform
// rendering/validation primitives those would be built on top of.
package subagent

import (
	"fmt"
	"regexp"
)

// NamePattern mirrors subagent.py's SUBAGENT_NAME_PATTERN.
var NamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

const (
	DefaultSubagentsConfig = "subagents"
	BackupDir              = ".local/state/aikito/backups"
)

// Definition mirrors subagent.py's SubagentDefinition: the canonical,
// workspace-side representation of one subagents/<name>.md file, already
// parsed (e.g. by workspace.ParseSubagentText) into typed fields.
type Definition struct {
	Name            string
	Description     string
	Agents          []string
	PlatformConfigs map[string]map[string]any
	Instructions    string
}

// ConfigError mirrors subagent_adapters.py's SubagentConfigError: raised
// when subagent configuration or target file operations fail.
type ConfigError struct{ Message string }

func (e *ConfigError) Error() string { return e.Message }

func configErrorf(format string, args ...any) error {
	return &ConfigError{Message: fmt.Sprintf(format, args...)}
}
