package cli

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-go/internal/linkplan"
)

// subagentRow ports render.py's SubagentRow.
type subagentRow struct {
	Name     string
	Statuses map[string]string // agent display name -> status
}

// orphanSubagentFile ports render.py's OrphanSubagentFile.
type orphanSubagentFile struct {
	AgentDisplayName, FilePath string
}

// collectSubagentsMatrix ports status.py collect_subagents_matrix: rows per
// subagent, orphan files, and agent display names in registry order. A
// subagent configuration error yields no rows (as in Python); an agent
// registry error is returned.
func collectSubagentsMatrix(c *inspectionContext) ([]subagentRow, []orphanSubagentFile, []string, error) {
	views, _, serr := c.subagentViews()
	if serr != nil {
		views = nil
	}
	reg, _, err := c.agents()
	if err != nil {
		return nil, nil, nil, err
	}
	agents := reg.Values()
	var names []string
	display := map[string]string{}
	for _, a := range agents {
		names = append(names, a.DisplayName)
		display[a.Name] = a.DisplayName
	}
	byName := map[string]map[string]string{}
	var orphans []orphanSubagentFile
	for _, v := range views {
		agDisplay := v.Agent
		if d, ok := display[v.Agent]; ok && v.Agent != "" {
			agDisplay = d
		}
		if v.Status == linkplan.StatusOrphan {
			rel := v.TargetPath
			if v.TargetPath != "" {
				if r, err := filepath.Rel(c.home, v.TargetPath); err == nil && r != ".." && !strings.HasPrefix(r, "../") {
					rel = "~/" + filepath.ToSlash(r)
				}
			}
			orphans = append(orphans, orphanSubagentFile{agDisplay, rel})
			continue
		}
		if v.ResourceName == "*" {
			continue
		}
		if byName[v.ResourceName] == nil {
			byName[v.ResourceName] = map[string]string{}
		}
		byName[v.ResourceName][agDisplay] = v.Status
	}
	var rows []subagentRow
	for name, st := range byName {
		for _, d := range names {
			if _, ok := st[d]; !ok {
				st[d] = "SKIP"
			}
		}
		rows = append(rows, subagentRow{name, st})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, orphans, names, nil
}
