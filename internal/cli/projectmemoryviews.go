package cli

import (
	"os"
	"path/filepath"

	"github.com/mr-miles/aikito-go/internal/linkplan"
)

// projectMemoryViews is WorkspaceInspectionContext.project_memory_views:
// plan_project_memory(build_project_memory_batch(...)).inspect() for one
// active checkout.
//
// TODO(project sync port): this is a reduced stand-in covering the main
// path of memory_runtime.py (project notes plus memory references; missing
// target -> MISSING, link to the canonical source -> OK, anything else ->
// CONFLICT). Replace it with the full memory_runtime port's Inspect once
// that lands, keeping this signature.
var projectMemoryViews = func(aikitoDir, projectName string, cfg map[string]any, checkout string) []linkplan.View {
	type resource struct{ rel, canonical string }
	var resources []resource
	seen := map[string]bool{}
	projMem := filepath.Join(aikitoDir, "projects", projectName, "memory")
	notes := filepath.Join(aikitoDir, "memory", projectName, "notes")
	if _, err := os.Lstat(projMem); err == nil {
		notes = filepath.Join(projMem, "notes")
	}
	if st, err := os.Stat(notes); err == nil && st.IsDir() {
		resources = append(resources, resource{"notes", notes})
		seen["notes"] = true
	}
	if refs, ok := cfg["memory"].([]any); ok {
		for _, r := range refs {
			ref := pyStr(r)
			if seen[ref] {
				continue
			}
			seen[ref] = true
			resources = append(resources, resource{ref, filepath.Join(aikitoDir, "memory", ref)})
		}
	}

	view := func(rel, target, canonical, status, reason string) linkplan.View {
		return linkplan.View{
			ResourceType: "project_memory", ResourceName: rel, Status: status, Scope: "project",
			Project: projectName, TargetPath: target, SourcePath: canonical, Reason: reason,
		}
	}
	agentsDir := filepath.Join(checkout, ".agents")
	runtimeDir := filepath.Join(agentsDir, "memory")
	if isSymlinkPath(agentsDir) {
		return []linkplan.View{view("", agentsDir, "", linkplan.StatusConflict, "Checkout .agents directory cannot be a symlink: "+agentsDir)}
	}
	if isSymlinkPath(runtimeDir) {
		return []linkplan.View{view("", runtimeDir, "", linkplan.StatusConflict, "Checkout .agents/memory directory cannot be a symlink: "+runtimeDir)}
	}
	var views []linkplan.View
	for _, r := range resources {
		target := filepath.Join(runtimeDir, r.rel)
		if _, err := os.Stat(r.canonical); err != nil {
			views = append(views, view(r.rel, target, r.canonical, linkplan.StatusConflict, "Project memory source does not exist: "+r.canonical))
			continue
		}
		obs := linkplan.Inspect(target, r.canonical, linkplan.InspectOptions{TargetKind: "memory_link", Scope: "project"})
		switch {
		case obs.EntryType == linkplan.EntryMissing:
			views = append(views, view(r.rel, target, r.canonical, linkplan.StatusMissing, "Create symbolic link to canonical memory "+r.canonical))
		case obs.EntryType == linkplan.EntrySymlink && obs.LinkPointsToCanonical:
			views = append(views, view(r.rel, target, r.canonical, linkplan.StatusOK, "Symbolic link already points to "+r.canonical))
		default:
			views = append(views, view(r.rel, target, r.canonical, linkplan.StatusConflict, ""))
		}
	}
	return views
}

func isSymlinkPath(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode()&os.ModeSymlink != 0
}
