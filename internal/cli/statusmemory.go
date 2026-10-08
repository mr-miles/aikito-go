package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/mr-miles/aikito-go/internal/linkplan"
	"github.com/mr-miles/aikito-go/internal/project"
)

// memoryStatusRow ports render.py's MemoryStatusRow.
type memoryStatusRow struct {
	Name, Scope, Status string
	NotesCount          int
	UpdatedOn           *time.Time
}

func notesGlob(dir string) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	return matches
}

func isDirPath(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// latestMemoryUpdate is the local date of the newest notes/*.md mtime.
func latestMemoryUpdate(memoryDir string) *time.Time {
	notes := filepath.Join(memoryDir, "notes")
	if !isDirPath(notes) {
		return nil
	}
	var latest time.Time
	found := false
	for _, p := range notesGlob(notes) {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if !found || st.ModTime().After(latest) {
			latest, found = st.ModTime(), true
		}
	}
	if !found {
		return nil
	}
	d := time.Date(latest.Year(), latest.Month(), latest.Day(), 0, 0, 0, 0, time.Local)
	return &d
}

// collectMemoryStatusRows ports status.py collect_memory_status_rows: the
// rows, the total note count and the memory issue count.
func collectMemoryStatusRows(c *inspectionContext, warn func(string)) ([]memoryStatusRow, int, int) {
	var rows []memoryStatusRow
	total, issues := 0, 0

	globalMem := filepath.Join(c.aikitoDir, "memory")
	notes := filepath.Join(globalMem, "notes")
	status := "OK"
	if !isDirPath(notes) {
		status = "MISSING"
		issues++
	}
	count := 0
	if isDirPath(notes) {
		count = len(notesGlob(notes))
	}
	total += count
	rows = append(rows, memoryStatusRow{Name: "Global", Scope: "Global", Status: status, NotesCount: count, UpdatedOn: latestMemoryUpdate(globalMem)})

	entries, err := os.ReadDir(filepath.Join(c.aikitoDir, "projects"))
	if err != nil {
		return rows, total, issues
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		projDir := filepath.Join(c.aikitoDir, "projects", name)
		if !isDirPath(projDir) {
			continue
		}
		projMem := filepath.Join(projDir, "memory")
		projNotes := filepath.Join(projMem, "notes")
		canonical := "OK"
		if !isDirPath(projNotes) {
			canonical = "MISSING"
			issues++
		}
		n := 0
		if isDirPath(projNotes) {
			n = len(notesGlob(projNotes))
		}
		total += n

		linkStatus := "N/A"
		agentToml := filepath.Join(projDir, "agent.toml")
		if st, err := os.Stat(agentToml); err == nil && st.Mode().IsRegular() {
			var cfg map[string]any
			data, rerr := os.ReadFile(agentToml)
			if rerr == nil {
				rerr = toml.Unmarshal(data, &cfg)
			}
			if rerr != nil {
				warn(fmt.Sprintf("[WARN] Failed to read configuration for project '%s': %v", name, rerr))
			} else {
				binding := project.ResolveProjectBinding(cfg, c.home)
				if active := binding.ActiveEntries(); len(active) > 0 {
					var statuses []string
					for _, entry := range active {
						views := projectMemoryViews(c.aikitoDir, name, cfg, entry.ResolvedPath)
						st := "OK"
						for _, v := range views {
							if v.Status == linkplan.StatusConflict {
								st = "CONFLICT"
								break
							}
							if v.Status == linkplan.StatusMissing {
								st = "MISSING"
							}
						}
						statuses = append(statuses, st)
					}
					linkStatus = "OK"
					for _, want := range []string{"CONFLICT", "MISSING"} {
						if containsString(statuses, want) {
							linkStatus = want
							issues++
							break
						}
					}
				} else if len(binding.OfflineEntries()) > 0 {
					linkStatus = "OFFLINE"
				}
			}
		}
		rowStatus := canonical
		if canonical == "OK" {
			rowStatus = linkStatus
		}
		rows = append(rows, memoryStatusRow{Name: name, Scope: "Project", Status: rowStatus, NotesCount: n, UpdatedOn: latestMemoryUpdate(projMem)})
	}
	return rows, total, issues
}
