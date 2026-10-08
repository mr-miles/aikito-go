package linkplan

import "path/filepath"

// Inspection statuses (inspection.py InspectionStatus).
const (
	StatusOK       = "OK"
	StatusMissing  = "MISSING"
	StatusUpdate   = "UPDATE"
	StatusRemove   = "REMOVE"
	StatusDrift    = "DRIFT"
	StatusConflict = "CONFLICT"
	StatusSkip     = "SKIP"
	StatusError    = "ERROR"
	StatusOrphan   = "ORPHAN"
)

// View ports inspection.py's ResourceInspectionView, keeping only the
// fields and details the reporting commands read.
type View struct {
	ResourceType string
	ResourceName string
	Status       string
	Scope        string
	Agent        string
	Project      string
	TargetPath   string
	SourcePath   string
	Reason       string

	// details
	ExpectedRepresentation string
	DesiredRepresentation  string
	SharedTarget           bool
}

func targetAgent(targets []targetLike, opTarget, fallback string) string {
	for _, t := range targets {
		if t.path == opTarget || IsSameTargetLocation(t.path, opTarget) {
			if len(t.consumers) > 0 && t.consumers[0] != "" {
				return t.consumers[0]
			}
			return fallback
		}
	}
	return fallback
}

type targetLike struct {
	path      string
	consumers []string
}

// Inspect ports GlobalSkillBatchPlan.inspect.
func (p GlobalSkillPlan) Inspect() []View {
	var views []View
	cop := p.ContainerOp
	cStatus := StatusError
	switch cop.Action {
	case ActNoop:
		cStatus = StatusOK
	case ActCreate:
		cStatus = StatusMissing
	case ActConflict:
		cStatus = StatusConflict
	case ActMigrateContainer:
		cStatus = StatusUpdate
	}
	views = append(views, View{
		ResourceType: "global_skill_container", ResourceName: filepath.Base(p.Batch.ContainerPath),
		Status: cStatus, Scope: "global", TargetPath: cop.TargetPath, Reason: cop.Reason,
	})
	for _, op := range p.EntryOps {
		st := StatusError
		switch op.Action {
		case ActNoop:
			st = StatusOK
		case ActCreate:
			st = StatusMissing
		case ActConflict:
			st = StatusConflict
		case ActSkip:
			st = StatusSkip
		case ActUnlink:
			st = StatusOrphan
		}
		views = append(views, View{
			ResourceType: "global_skill_entry", ResourceName: op.ResourceName, Status: st, Scope: "global",
			TargetPath: op.TargetPath, Reason: op.Reason, DesiredRepresentation: op.DesiredRepresentation,
		})
	}
	var targets []targetLike
	for _, c := range p.Batch.Consumers {
		targets = append(targets, targetLike{c.Path, c.Consumers})
	}
	for _, op := range p.ConsumerOps {
		st := StatusError
		switch op.Action {
		case ActNoop, ActSharedPath:
			st = StatusOK
		case ActCreate:
			st = StatusMissing
		case ActConflict:
			st = StatusConflict
		case ActSkip:
			st = StatusSkip
		}
		views = append(views, View{
			ResourceType: "global_skill_consumer", ResourceName: op.ResourceName, Status: st, Scope: "global",
			Agent: targetAgent(targets, op.TargetPath, op.ResourceName), TargetPath: op.TargetPath,
			Reason: op.Reason, SharedTarget: op.Action == ActSharedPath,
		})
	}
	return views
}

// Inspect ports InstructionPlan.inspect.
func (p InstructionPlan) Inspect() []View {
	var targets []targetLike
	for _, t := range p.Batch.Targets {
		targets = append(targets, targetLike{t.Path, t.Consumers})
	}
	var views []View
	for _, op := range p.Operations {
		st := StatusError
		switch op.Action {
		case ActNoop, ActSharedPath:
			st = StatusOK
		case ActCreate, ActUnlink:
			st = StatusMissing
		case ActConflict:
			st = StatusConflict
		case ActSkip:
			st = StatusSkip
		}
		name := op.ResourceName
		if name == "" {
			name = "AGENTS.md"
		}
		views = append(views, View{
			ResourceType: "instruction", ResourceName: name, Status: st, Scope: p.Batch.Scope,
			Agent: targetAgent(targets, op.TargetPath, op.ResourceName), Project: p.Batch.ProjectName,
			TargetPath: op.TargetPath, SourcePath: p.Batch.CanonicalSource, Reason: op.Reason,
			ExpectedRepresentation: op.ExpectedRepresentation,
		})
	}
	return views
}
