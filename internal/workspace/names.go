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
