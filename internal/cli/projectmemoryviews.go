package cli

import (
	"os"

	"github.com/mr-miles/aikito-go/internal/linkplan"
	"github.com/mr-miles/aikito-go/internal/projectsync"
)

// projectMemoryPlan is build_project_memory_batch + plan_project_memory for
// one active checkout.
func projectMemoryPlan(aikitoDir, projectName string, cfg map[string]any, checkout string) projectsync.MemoryPlan {
	batch := projectsync.BuildProjectMemoryBatch(aikitoDir, projectName, cfg, []string{checkout}, nil)
	return projectsync.PlanProjectMemory(batch)
}

// projectMemoryViews is WorkspaceInspectionContext.project_memory_views
// (MemoryPlan.inspect).
func projectMemoryViews(aikitoDir, projectName string, cfg map[string]any, checkout string) []linkplan.View {
	plan := projectMemoryPlan(aikitoDir, projectName, cfg, checkout)
	var views []linkplan.View
	for _, op := range plan.Operations {
		st := linkplan.StatusError
		switch op.Action {
		case linkplan.ActNoop, linkplan.ActSharedPath:
			st = linkplan.StatusOK
		case linkplan.ActCreate:
			st = linkplan.StatusMissing
		case linkplan.ActUnlink, linkplan.ActConflict:
			st = linkplan.StatusConflict
		case linkplan.ActSkip:
			st = linkplan.StatusSkip
		}
		views = append(views, linkplan.View{
			ResourceType: "project_memory", ResourceName: op.ResourceName, Status: st, Scope: "project",
			Project: projectName, TargetPath: op.TargetPath, SourcePath: op.CanonicalPath, Reason: op.Reason,
		})
	}
	return views
}

func isSymlinkPath(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode()&os.ModeSymlink != 0
}
