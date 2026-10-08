package project

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-go/internal/workspace"
)

// ContextConflictError is resolve.py's ProjectContextConflictError: the
// current directory belongs to several projects equally.
type ContextConflictError struct {
	Path     string
	Projects []string
}

func (e *ContextConflictError) Error() string {
	return fmt.Sprintf("Current directory '%s' belongs to multiple projects: %s", e.Path, strings.Join(e.Projects, ", "))
}

func isUnder(p, base string) bool {
	return p == base || strings.HasPrefix(p, base+string(filepath.Separator)) || base == string(filepath.Separator)
}

func pathPartCount(p string) int {
	n := 1 // the root anchor
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part != "" {
			n++
		}
	}
	return n
}

// DetectCurrentProject is resolve.py's detect_current_project: the project
// whose workspace folder or longest active checkout path contains cwd.
// Returns "" when none matches.
func DetectCurrentProject(aikitoDir, cwd, home string) (string, error) {
	current, err := workspace.ResolvePath(cwd)
	if err != nil {
		return "", nil
	}
	projectsDir := filepath.Join(aikitoDir, "projects")
	if st, err := os.Stat(projectsDir); err != nil || !st.IsDir() {
		return "", nil
	}
	if resolvedProj, err := workspace.ResolvePath(projectsDir); err == nil && current != resolvedProj && isUnder(current, resolvedProj) {
		rel, _ := filepath.Rel(resolvedProj, current)
		candidate := strings.Split(filepath.ToSlash(rel), "/")[0]
		if candidate != "" && !strings.HasPrefix(candidate, ".") {
			if st, err := os.Stat(filepath.Join(projectsDir, candidate)); err == nil && st.IsDir() {
				if st, err := os.Stat(filepath.Join(projectsDir, candidate, "agent.toml")); err == nil && st.Mode().IsRegular() {
					return candidate, nil
				}
			}
		}
	}

	type match struct{ name, path string }
	var matches []match
	entries, _ := os.ReadDir(projectsDir)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		folder := filepath.Join(projectsDir, name)
		if st, err := os.Stat(folder); err != nil || !st.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		cfgPath := filepath.Join(folder, "agent.toml")
		if st, err := os.Stat(cfgPath); err != nil || !st.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(cfgPath)
		if err != nil {
			continue
		}
		cfg, err := workspace.DecodeTOML(data)
		if err != nil {
			continue
		}
		for _, e := range ResolveProjectBinding(cfg, home).ActiveEntries() {
			resolved, err := workspace.ResolvePath(e.ResolvedPath)
			if err != nil {
				continue
			}
			if isUnder(current, resolved) {
				matches = append(matches, match{name, resolved})
			}
		}
	}
	if len(matches) == 0 {
		return "", nil
	}
	maxLen := 0
	for _, m := range matches {
		if n := pathPartCount(m.path); n > maxLen {
			maxLen = n
		}
	}
	unique := map[string]bool{}
	var best []string
	for _, m := range matches {
		if pathPartCount(m.path) == maxLen && !unique[m.name] {
			unique[m.name] = true
			best = append(best, m.name)
		}
	}
	sort.Strings(best)
	if len(best) == 1 {
		return best[0], nil
	}
	return "", &ContextConflictError{Path: current, Projects: best}
}
