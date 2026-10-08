package linkplan

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-go/internal/registry"
)

// GlobalSkillBatch ports global_skills.py's GlobalSkillBatch: the managed
// container (~/.agents/skills), one entry per selected skill, stale entries
// found in the container, and the deduplicated consumer targets.
type GlobalSkillBatch struct {
	ContainerPath   string
	CanonicalRoot   string
	SelectedEntries []string // skill names
	StaleEntries    []string // entry names found in the container
	Consumers       []registry.Target
}

// ConsumerAgentCount is the summary's "across N consumers": agents with a
// skills_path.
func (b GlobalSkillBatch) ConsumerAgentCount() int {
	n := 0
	for _, c := range b.Consumers {
		n += len(c.Consumers)
	}
	return n
}

// BuildGlobalSkillBatch ports build_global_skill_batch.
func BuildGlobalSkillBatch(aikitoDir, home string, skills []string, reg *registry.AgentRegistry, containerPath string) (GlobalSkillBatch, error) {
	selected := map[string]bool{}
	for _, s := range skills {
		selected[s] = true
	}
	b := GlobalSkillBatch{
		ContainerPath:   containerPath,
		CanonicalRoot:   filepath.Join(aikitoDir, "skills"),
		SelectedEntries: skills,
	}
	if isDir(containerPath) && !isSymlink(containerPath) {
		if entries, err := os.ReadDir(containerPath); err == nil {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			sort.Strings(names)
			for _, n := range names {
				if !selected[n] {
					b.StaleEntries = append(b.StaleEntries, n)
				}
			}
		}
	}
	consumers, err := registry.ResolveTargets("global_skills", aikitoDir, home, reg, registry.ResolveTargetsOptions{})
	if err != nil {
		return b, err
	}
	b.Consumers = consumers
	return b, nil
}

// GlobalSkillPlan ports GlobalSkillBatchPlan.
type GlobalSkillPlan struct {
	Batch       GlobalSkillBatch
	ContainerOp Operation
	EntryOps    []Operation
	ConsumerOps []Operation
}

// AllOperations is container, entries, consumers in plan order.
func (p GlobalSkillPlan) AllOperations() []Operation {
	ops := append([]Operation{p.ContainerOp}, p.EntryOps...)
	return append(ops, p.ConsumerOps...)
}

// Conflicts returns the CONFLICT operations, in plan order.
func (p GlobalSkillPlan) Conflicts() []Operation { return conflicts(p.AllOperations()) }

// CanApply is GlobalSkillBatchPlan.can_apply.
func (p GlobalSkillPlan) CanApply() bool { return canApply(p.AllOperations()) }

// PlanGlobalSkills ports plan_global_skills. refreshed names bundled skills
// whose canonical source will be regenerated before links are applied.
func PlanGlobalSkills(b GlobalSkillBatch, home string, refreshed map[string]bool) GlobalSkillPlan {
	isLegacy := isSymlink(b.ContainerPath)
	containerObs := Inspect(b.ContainerPath, b.CanonicalRoot, InspectOptions{TargetKind: ManagedContainer, Scope: "global"})
	containerOp := Plan(containerObs, "link", PlanOptions{IsLegacyContainer: isLegacy})
	migrating := containerOp.Action == ActMigrateContainer

	plan := GlobalSkillPlan{Batch: b, ContainerOp: containerOp}
	for _, name := range b.SelectedEntries {
		entry := filepath.Join(b.ContainerPath, name)
		canonical := filepath.Join(b.CanonicalRoot, name)
		valid, errText := true, ""
		switch {
		case refreshed[name] && !exists(canonical):
		case !exists(canonical):
			valid, errText = false, "Canonical skill source does not exist: "+canonical
		case !isDir(canonical):
			valid, errText = false, "Canonical skill source is not a directory: "+canonical
		}
		var obs ObservedLink
		if migrating {
			obs = ObservedLink{
				TargetPath: entry, EntryType: EntryMissing, ExpectedCanonical: canonical,
				CanonicalValid: valid, CanonicalError: errText, TargetKind: ManagedEntry, Scope: "global",
			}
		} else {
			obs = Inspect(entry, canonical, InspectOptions{
				CanonicalInvalid: !valid, CanonicalError: errText, TargetKind: ManagedEntry, Scope: "global",
			})
		}
		plan.EntryOps = append(plan.EntryOps, Plan(obs, "link", PlanOptions{ResourceName: name}))
	}
	for _, name := range b.StaleEntries {
		obs := Inspect(filepath.Join(b.ContainerPath, name), filepath.Join(b.CanonicalRoot, name),
			InspectOptions{TargetKind: ManagedEntry, Scope: "global"})
		plan.EntryOps = append(plan.EntryOps, Plan(obs, "absent", PlanOptions{ResourceName: name}))
	}
	for _, c := range b.Consumers {
		avail := registry.CheckTargetAvailability(c, home)
		parentExists := exists(filepath.Dir(c.Path))
		var obs ObservedLink
		if migrating && c.Path == b.ContainerPath {
			obs = ObservedLink{
				TargetPath: c.Path, EntryType: EntryDir, ExpectedCanonical: c.CanonicalSource, CanonicalValid: true,
				LinkPointsToCanonical: true, IsSameObject: true, TargetKind: ConsumerLink, Scope: "global",
			}
		} else {
			obs = Inspect(c.Path, c.CanonicalSource, InspectOptions{
				TargetKind: ConsumerLink, Scope: "global", IsSameObject: c.IsSameObject(),
			})
		}
		plan.ConsumerOps = append(plan.ConsumerOps, Plan(obs, "link", PlanOptions{
			AvailabilityStatus: avail.Status, ParentExists: &parentExists,
			ResourceName: strings.Join(c.ConsumerDisplayNames, "/"),
		}))
	}
	return plan
}

// GlobalSkillExecutionResult ports GlobalSkillExecutionResult.
type GlobalSkillExecutionResult struct {
	Success             bool
	ConsumerTargetCount int
	ExecutedChangeCount int
	ErrorMessage        string
}

// ExecuteGlobalSkills ports execute_global_skills: container, then stale
// cleanups, then selected entries, then consumer links.
func ExecuteGlobalSkills(plan GlobalSkillPlan, dryRun, verbose bool, stdout, stderr io.Writer) GlobalSkillExecutionResult {
	res := GlobalSkillExecutionResult{ConsumerTargetCount: len(plan.Batch.Consumers)}
	var ordered []Operation
	ordered = append(ordered, plan.ContainerOp)
	for _, op := range plan.EntryOps {
		if op.DesiredRepresentation == "absent" {
			ordered = append(ordered, op)
		}
	}
	for _, op := range plan.EntryOps {
		if op.DesiredRepresentation == "link" {
			ordered = append(ordered, op)
		}
	}
	ordered = append(ordered, plan.ConsumerOps...)
	for _, op := range ordered {
		r := Apply(op, dryRun, verbose, stdout, stderr)
		if r.Applied {
			res.ExecutedChangeCount++
		}
		if !r.Success {
			res.ErrorMessage = r.ErrorMessage
			return res
		}
	}
	res.Success = true
	return res
}
