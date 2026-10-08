package workspace

import (
	"fmt"
	"regexp"
	"strings"
)

// namePattern mirrors add.py's NAME_PATTERN exactly: kebab-case, must start
// and end with an alphanumeric character (no leading/trailing hyphen).
var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// ValidateResourceName mirrors add.py's validate_resource_name: returns ""
// when name is valid for resourceType, else a user-facing error message.
func ValidateResourceName(name, resourceType string) string {
	if name == "" || strings.TrimSpace(name) == "" {
		return fmt.Sprintf("%s name cannot be empty.", capitalizePy(resourceType))
	}
	nameClean := strings.TrimSpace(name)
	if strings.Contains(nameClean, "/") || strings.Contains(nameClean, `\`) ||
		strings.Contains(nameClean, "\x00") || strings.Contains(nameClean, "..") {
		return fmt.Sprintf("Invalid %s name '%s'. Path separators and traversals are not allowed.", resourceType, name)
	}
	if !namePattern.MatchString(nameClean) {
		return fmt.Sprintf(
			"Invalid %s name '%s'. Must be kebab-case (lowercase alphanumeric characters separated by hyphens, e.g. 'my-%s').",
			resourceType, name, resourceType,
		)
	}
	return ""
}

// capitalizePy mirrors Python's str.capitalize(): first rune upper-cased,
// the rest lower-cased.
func capitalizePy(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
}

// ValidateMemoryName mirrors memory.py's validate_memory_name: kebab-case,
// non-empty, no path separators/traversal, and at most 50 characters.
func ValidateMemoryName(name string) string {
	if name == "" || strings.TrimSpace(name) == "" {
		return "Memory note name cannot be empty."
	}
	nameClean := strings.TrimSpace(name)
	if strings.Contains(nameClean, "/") || strings.Contains(nameClean, `\`) ||
		strings.Contains(nameClean, "\x00") || strings.Contains(nameClean, "..") {
		return fmt.Sprintf("Invalid memory note name '%s'. Path separators and traversals are not allowed.", name)
	}
	if len([]rune(nameClean)) > 50 {
		return fmt.Sprintf("Invalid memory note name '%s'. Length must be at most 50 characters (got %d).", name, len([]rune(nameClean)))
	}
	if !namePattern.MatchString(nameClean) {
		return fmt.Sprintf(
			"Invalid memory note name '%s'. Must be kebab-case (lowercase alphanumeric characters separated by hyphens, e.g. 'payment-idempotency').",
			name,
		)
	}
	return ""
}

// projectNamePattern mirrors init.py's _validate_project_name regex: laxer
// than the kebab-case resource pattern — allows uppercase and dots.
var projectNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateProjectName mirrors init.py's _validate_project_name.
func ValidateProjectName(name string) string {
	if name == "" || !projectNamePattern.MatchString(name) {
		return "Project name must start with a letter or digit and contain only " +
			"letters, digits, dots, underscores, or hyphens."
	}
	return ""
}

// SubagentNamePattern mirrors subagent.py's SUBAGENT_NAME_PATTERN.
var SubagentNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// BundledSkillNames mirrors templating.py's BUNDLED_SKILL_NAMES: system-
// managed skill snapshots that are never treated as ordinary user resources.
var BundledSkillNames = map[string]struct{}{"aikito": {}, "durable-memory": {}}

func IsBundledSkillName(name string) bool {
	_, ok := BundledSkillNames[name]
	return ok
}
