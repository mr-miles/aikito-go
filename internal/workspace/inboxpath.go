package workspace

import (
	"path/filepath"
	"strings"
)

// GetInboxPath ports config.py get_inbox_path: the configured inbox
// directory (config.toml [inbox] path), resolved. Defaults to
// <workspace>/inbox; relative paths are taken from the workspace. Unlike
// the resource scanner, the result may lie outside the workspace.
func GetInboxPath(aikitoDir, home string) string {
	raw := strings.TrimSpace(readInboxConfigPath(aikitoDir))
	resolve := func(p string) string {
		r, err := ResolvePath(p)
		if err != nil {
			return p
		}
		return r
	}
	if raw == "" || raw == "inbox" {
		return resolve(filepath.Join(aikitoDir, "inbox"))
	}
	expanded := ExpandUser(home, raw)
	if expanded == ExpandUser(home, legacyDefaultInboxPath) {
		return resolve(filepath.Join(aikitoDir, "inbox"))
	}
	if !filepath.IsAbs(expanded) {
		return resolve(filepath.Join(aikitoDir, expanded))
	}
	return resolve(expanded)
}
