package mcp

import (
	"fmt"
	"os"
	"sort"

	"github.com/mr-miles/aikito-go/internal/sync"
)

// MCPOperation is a planned logical mutation for an MCP server in an agent
// config (planner.py's MCPOperation). Action is one of NOOP/CREATE/UPDATE/
// REMOVE/CONFLICT/SKIP/ERROR.
type MCPOperation struct {
	Target          MCPConfigTarget
	Action          string
	Reason          string
	Observed        *MCPObservedEntry
	Desired         *MCPDesiredEntry
	RequiresForce   bool
	ForceIdentity   string
	IsAuthorized    bool
	StateTransition *StateTransition
	Spec            *AgentSpec
}

// StateTransition is (state_key, new_fingerprint_or_nil) — nil FP means
// "clear this state key" (used only for REMOVE/absent operations).
type StateTransition struct {
	StateKey    string
	Fingerprint *string
}

// IsDrift mirrors MCPOperation.is_drift.
func (op MCPOperation) IsDrift() bool { return op.RequiresForce || op.Action == "CONFLICT" }

// MCPFilePlan aggregates all operations targeting one physical agent config
// file.
type MCPFilePlan struct {
	Path             string
	PhysicalIdentity string
	Format           string
	Sensitive        bool
	PreImage         FileSnapshot
	Operations       []MCPOperation
	OrigContent      *string
	FinalContent     *string
}

// WillMutate mirrors MCPFilePlan.will_mutate.
func (fp MCPFilePlan) WillMutate() bool {
	for _, op := range fp.Operations {
		if (op.Action == "CREATE" || op.Action == "UPDATE" || op.Action == "REMOVE") && op.IsAuthorized {
			return true
		}
	}
	return false
}

// ShouldBackup mirrors MCPFilePlan.should_backup.
func (fp MCPFilePlan) ShouldBackup() bool {
	return fp.WillMutate() && fp.PreImage.Exists && !fp.Sensitive &&
		fp.Format != "claude_json" && fp.Format != "agy_json"
}

// ValidatePrecondition mirrors MCPFilePlan.validate_precondition.
func (fp MCPFilePlan) ValidatePrecondition() error {
	ok, msg := fp.PreImage.ValidatePrecondition(fp.Path)
	if !ok {
		return &StaleConfigPlanError{Message: msg}
	}
	return nil
}

// MCPPlan is the immutable, fully-evaluated synchronization plan for MCP
// servers.
type MCPPlan struct {
	Operations        []MCPOperation
	FilePlans         []MCPFilePlan
	StateSnapshotHash string
	Specs             []AgentSpec
}

func (p MCPPlan) CanApply() bool {
	for _, op := range p.Operations {
		if (op.Action == "CONFLICT" && !op.IsAuthorized) || op.Action == "ERROR" {
			return false
		}
	}
	return true
}

func (p MCPPlan) ChangesCount() int {
	n := 0
	for _, op := range p.Operations {
		if (op.Action == "CREATE" || op.Action == "UPDATE" || op.Action == "REMOVE") && op.IsAuthorized {
			n++
		}
	}
	return n
}

func (p MCPPlan) ConflictsCount() int {
	n := 0
	for _, op := range p.Operations {
		if op.Action == "CONFLICT" && !op.IsAuthorized {
			n++
		}
	}
	return n
}

func (p MCPPlan) HasConflicts() bool { return p.ConflictsCount() > 0 }

// ValidatePreconditions mirrors MCPPlan.validate_preconditions: re-hash the
// state file and re-check every file plan's pre-image.
func (p MCPPlan) ValidatePreconditions(home string) error {
	statePath := home + "/" + StateFile
	currHash := stateFileHash(statePath)
	if currHash != p.StateSnapshotHash {
		return &StaleConfigPlanError{Message: fmt.Sprintf(
			"MCP state store '%s' has been modified externally since plan generation", statePath)}
	}
	for _, fp := range p.FilePlans {
		if err := fp.ValidatePrecondition(); err != nil {
			return err
		}
	}
	return nil
}

// --- plan -> cross-domain observation (plan_observation.go's shared vocabulary) ---

// MCPOperationEffect mirrors mcp_operation_effect.
func MCPOperationEffect(op MCPOperation) (sync.OperationEffect, error) {
	if (op.Action == "CREATE" || op.Action == "UPDATE" || op.Action == "REMOVE") && !op.IsAuthorized {
		return sync.EffectNone, nil
	}
	switch op.Action {
	case "CREATE":
		return sync.EffectCreate, nil
	case "UPDATE":
		return sync.EffectUpdate, nil
	case "REMOVE":
		return sync.EffectRemove, nil
	case "NOOP":
		return sync.EffectNoop, nil
	case "SKIP":
		return sync.EffectSkip, nil
	case "CONFLICT":
		if op.IsAuthorized {
			return "", &sync.UnknownPlanActionError{Message: fmt.Sprintf("Authorized CONFLICT is invalid for MCP: %s", op.Target.LogicalIdentity)}
		}
		return sync.EffectNone, nil
	case "ERROR":
		return sync.EffectNone, nil
	default:
		return "", &sync.UnknownPlanActionError{Message: "Unhandled MCP action: " + op.Action}
	}
}

// MCPOperationFinding mirrors mcp_operation_finding.
func MCPOperationFinding(op MCPOperation) *sync.Finding {
	if (op.Action == "CONFLICT" && !op.IsAuthorized) ||
		((op.Action == "CREATE" || op.Action == "UPDATE" || op.Action == "REMOVE") && !op.IsAuthorized) {
		return &sync.Finding{
			Status:   "CONFLICT",
			Code:     "MCP_CONFLICT",
			Message:  fmt.Sprintf("%s/%s: %s", op.Target.Agent, op.Target.LogicalIdentity, op.Reason),
			Resource: op.Target.Path,
		}
	}
	if op.Action == "ERROR" {
		return &sync.Finding{
			Status:   "ERROR",
			Code:     "MCP_ERROR",
			Message:  fmt.Sprintf("%s/%s: %s", op.Target.Agent, op.Target.LogicalIdentity, op.Reason),
			Resource: op.Target.Path,
		}
	}
	return nil
}

// ObserveMCPOperation mirrors observe_mcp_operation: a panicking/erroring
// effect mapping becomes an ERROR finding + effect "none", never a crash.
func ObserveMCPOperation(op MCPOperation) (sync.PlanOperationView, *sync.Finding) {
	effect, err := MCPOperationEffect(op)
	var finding *sync.Finding
	if err != nil {
		effect = sync.EffectNone
		finding = &sync.Finding{Status: "ERROR", Code: "UNKNOWN_PLAN_ACTION", Message: err.Error(), Resource: op.Target.Path}
	} else {
		finding = MCPOperationFinding(op)
	}
	view := sync.PlanOperationView{
		ResourceType: "mcp", ResourceName: op.Target.LogicalIdentity, Effect: effect,
		Scope: "global", Agent: op.Target.Agent, Target: op.Target.Path,
		Reason: op.Reason, DomainAction: op.Action, Authorized: op.IsAuthorized,
	}
	return view, finding
}

// --- build_mcp_plan ---

// BuildMCPPlanOptions mirrors build_mcp_plan's keyword-only parameters.
type BuildMCPPlanOptions struct {
	Specs                []AgentSpec // non-nil overrides loading from aikitoDir/home (test seam)
	Force                bool
	ForceTargets         map[string]bool
	DesiredAbsentServers map[string]bool
}

// BuildMCPPlan mirrors build_mcp_plan: the single decision engine shared by
// sync/status/diff/doctor. Port this once; every command-level feature
// should call it rather than reimplementing the decision tree.
func BuildMCPPlan(aikitoDir, home string, opts BuildMCPPlanOptions) (MCPPlan, error) {
	rawSpecs := opts.Specs
	if rawSpecs == nil {
		var err error
		rawSpecs, err = LoadAgentSpecs(aikitoDir, home)
		if err != nil {
			return MCPPlan{}, err
		}
	}
	absentServers := opts.DesiredAbsentServers
	if absentServers == nil {
		absentServers = map[string]bool{}
	}
	forceTargets := opts.ForceTargets
	if forceTargets == nil {
		forceTargets = map[string]bool{}
	}

	state, err := LoadState(home)
	if err != nil {
		return MCPPlan{}, err
	}
	stateSnapshotHash := stateFileHash(home + "/" + StateFile)

	type group struct {
		specs []AgentSpec
	}
	groups := map[string]*group{}
	var groupOrder []string
	canonicalPaths := map[string]string{}
	var unsupportedOps []MCPOperation

	for _, s := range rawSpecs {
		if s.ConfigFormat == "unsupported" {
			unsupportedOps = append(unsupportedOps, MCPOperation{
				Target: MCPConfigTarget{
					Path: s.ConfigPath, LogicalIdentity: s.Server, Format: s.ConfigFormat,
					Agent: s.Agent, TargetName: s.TargetName,
				},
				Action:       "SKIP",
				Reason:       orDefault(s.Reason, "MCP synchronization is not supported"),
				Spec:         specPtr(s),
				IsAuthorized: true,
			})
			continue
		}
		physID := ResolvePhysicalIdentity(s.ConfigPath)
		g, ok := groups[physID]
		if !ok {
			g = &group{}
			groups[physID] = g
			groupOrder = append(groupOrder, physID)
			canonicalPaths[physID] = s.ConfigPath
		}
		g.specs = append(g.specs, s)
	}

	// Collision checks.
	for _, physID := range groupOrder {
		gSpecs := groups[physID].specs
		canonicalPath := canonicalPaths[physID]
		formatSet := map[string]bool{}
		for _, s := range gSpecs {
			if s.ConfigFormat != "" {
				formatSet[s.ConfigFormat] = true
			}
		}
		if len(formatSet) > 1 {
			var formats []string
			for f := range formatSet {
				formats = append(formats, f)
			}
			sort.Strings(formats)
			return MCPPlan{}, collisionErrorf("Conflicting formats declared for physical file '%s': %v", canonicalPath, formats)
		}

		seenTargetNames := map[string]AgentSpec{}
		for _, s := range gSpecs {
			tName := s.TargetName
			isAbsent := absentServers[s.Server] || s.Desired == nil
			if prevS, ok := seenTargetNames[tName]; ok {
				prevIsAbsent := absentServers[prevS.Server] || prevS.Desired == nil
				if prevS.Server != s.Server {
					return MCPPlan{}, collisionErrorf("Colliding MCP server names: '%s' and '%s' both map to target name '%s' in '%s'", prevS.Server, s.Server, tName, canonicalPath)
				} else if prevIsAbsent != isAbsent {
					return MCPPlan{}, collisionErrorf("Conflicting operations on MCP server '%s' in '%s': conflicting REMOVE and UPDATE", tName, canonicalPath)
				}
			} else {
				seenTargetNames[tName] = s
			}
		}
	}

	var allOperations []MCPOperation
	var filePlans []MCPFilePlan

	for _, physID := range groupOrder {
		gSpecs := groups[physID].specs
		canonicalPath := canonicalPaths[physID]
		resolvedFormat := ""
		if len(gSpecs) > 0 {
			resolvedFormat = gSpecs[0].ConfigFormat
		}
		fileSensitive := false
		for _, s := range gSpecs {
			adapter, err := GetMCPAdapter(s.Adapter)
			if err == nil && (s.ContainsSecret || adapter.MaterializesSecrets) {
				fileSensitive = true
				break
			}
		}
		fileSnapshot := CaptureFileSnapshot(canonicalPath)
		fileExisted := fileSnapshot.Exists
		origText := ""
		if fileExisted {
			if data, err := readFileString(canonicalPath); err == nil {
				origText = data
			}
		}
		currentText := origText
		var groupOps []MCPOperation

		for _, spec := range gSpecs {
			targetKey := spec.Agent + "/" + spec.Server
			isAuthorized := opts.Force || forceTargets[targetKey] || forceTargets[spec.Server]
			isAbsent := absentServers[spec.Server] || spec.Desired == nil
			configTarget := MCPConfigTarget{
				Path: canonicalPath, LogicalIdentity: spec.Server, Format: spec.ConfigFormat,
				Agent: spec.Agent, TargetName: spec.TargetName,
				Sensitive: func() bool {
					adapter, err := GetMCPAdapter(spec.Adapter)
					return spec.ContainsSecret || (err == nil && adapter.MaterializesSecrets)
				}(),
			}

			var op MCPOperation
			if isAbsent {
				op, currentText = planAbsentOp(spec, configTarget, targetKey, isAuthorized, fileExisted, currentText, state)
			} else {
				op, currentText = planPresentOp(spec, configTarget, targetKey, isAuthorized, fileExisted, currentText, state)
			}
			groupOps = append(groupOps, op)
			allOperations = append(allOperations, op)
		}

		fileMutating := currentText != origText
		var origPtr, finalPtr *string
		if fileExisted {
			o := origText
			origPtr = &o
		}
		finalText := origText
		if fileMutating {
			finalText = currentText
		}
		finalPtr = &finalText
		filePlans = append(filePlans, MCPFilePlan{
			Path: canonicalPath, PhysicalIdentity: physID, Format: resolvedFormat,
			Sensitive: fileSensitive, PreImage: fileSnapshot, Operations: groupOps,
			OrigContent: origPtr, FinalContent: finalPtr,
		})
	}

	allOperations = append(allOperations, unsupportedOps...)
	return MCPPlan{
		Operations: allOperations, FilePlans: filePlans,
		StateSnapshotHash: stateSnapshotHash, Specs: rawSpecs,
	}, nil
}

// planAbsentOp mirrors build_mcp_plan's is_absent branch (a `rm` command, or
// spec.Desired == nil): returns the planned operation and the (possibly
// updated) running file-content buffer for the rest of this physical-file
// group to see.
func planAbsentOp(spec AgentSpec, configTarget MCPConfigTarget, targetKey string, isAuthorized, fileExisted bool, currentText string, state MCPState) (MCPOperation, string) {
	if !fileExisted {
		return MCPOperation{
			Target: configTarget, Action: "NOOP", Reason: "Target file does not exist",
			Observed: &MCPObservedEntry{Target: configTarget, Exists: false},
			Spec:     specPtr(spec), ForceIdentity: targetKey, IsAuthorized: true,
			StateTransition: &StateTransition{StateKey: spec.StateKey()},
		}, currentText
	}

	current, err := ReadEntry(spec, currentText)
	if err != nil {
		return MCPOperation{Target: configTarget, Action: "ERROR", Reason: "Failed to read entry: " + err.Error(), Spec: specPtr(spec), IsAuthorized: false}, currentText
	}

	managedFP := managedFingerprint(state, spec.StateKey())
	var currentFP *string
	if current != nil {
		fp := Fingerprint(current)
		currentFP = &fp
	}
	observed := &MCPObservedEntry{Target: configTarget, Exists: current != nil, Fingerprint: currentFP, ManagedFingerprint: managedFP, IsManaged: managedFP != nil, rawEntry: current}

	if current == nil {
		return MCPOperation{
			Target: configTarget, Action: "NOOP", Reason: "Already absent", Observed: observed,
			Spec: specPtr(spec), ForceIdentity: targetKey, IsAuthorized: true,
			StateTransition: &StateTransition{StateKey: spec.StateKey()},
		}, currentText
	}

	safeToRemove := isAuthorized || (managedFP != nil && currentFP != nil && *managedFP == *currentFP)
	if !safeToRemove {
		return MCPOperation{
			Target: configTarget, Action: "CONFLICT",
			Reason:   "Existing config was not last written by aikito; review it or rerun with --force",
			Observed: observed, Spec: specPtr(spec), RequiresForce: true, ForceIdentity: targetKey, IsAuthorized: false,
		}, currentText
	}

	requiresForce := managedFP == nil || currentFP == nil || *managedFP != *currentFP
	op := MCPOperation{
		Target: configTarget, Action: "REMOVE", Reason: "Removed from agent configuration",
		Observed: observed, Spec: specPtr(spec), RequiresForce: requiresForce, ForceIdentity: targetKey,
		IsAuthorized: true, StateTransition: &StateTransition{StateKey: spec.StateKey()},
	}
	newText, err := RemoveEntry(spec, currentText)
	if err != nil {
		return MCPOperation{Target: configTarget, Action: "ERROR", Reason: "Failed to remove entry: " + err.Error(), Spec: specPtr(spec), IsAuthorized: false}, currentText
	}
	return op, newText
}

// planPresentOp mirrors build_mcp_plan's "desired present" branch.
func planPresentOp(spec AgentSpec, configTarget MCPConfigTarget, targetKey string, isAuthorized, fileExisted bool, currentText string, state MCPState) (MCPOperation, string) {
	if !spec.Enabled {
		return MCPOperation{Target: configTarget, Action: "SKIP", Reason: orDefault(spec.Reason, "Agent or server disabled"), Spec: specPtr(spec), IsAuthorized: true}, currentText
	}
	if !AgentDetected(spec) {
		return MCPOperation{Target: configTarget, Action: "SKIP", Reason: fmt.Sprintf("Agent '%s' is not installed or detected", spec.Agent), Spec: specPtr(spec), IsAuthorized: true}, currentText
	}

	var current *OrderedObject
	if fileExisted {
		var err error
		current, err = ReadEntry(spec, currentText)
		if err != nil {
			return MCPOperation{Target: configTarget, Action: "ERROR", Reason: "Failed to read entry: " + err.Error(), Spec: specPtr(spec), IsAuthorized: false}, currentText
		}
	}

	managedFP := managedFingerprint(state, spec.StateKey())
	var currentFP *string
	if current != nil {
		fp := Fingerprint(current)
		currentFP = &fp
	}
	desiredFP := Fingerprint(spec.Desired)
	observed := &MCPObservedEntry{Target: configTarget, Exists: current != nil, Fingerprint: currentFP, ManagedFingerprint: managedFP, IsManaged: managedFP != nil, rawEntry: current}
	desiredEntry := &MCPDesiredEntry{
		Target: configTarget, Fingerprint: &desiredFP, ContainsSecret: spec.ContainsSecret,
		MissingCredentialEnv: spec.MissingCredentialEnv, LiveCommand: spec.LiveCommand, AuthCommand: spec.AuthCommand,
		rawDesired: spec.Desired,
	}

	if spec.MissingCredentialEnv != "" {
		return MCPOperation{
			Target: configTarget, Action: "SKIP", Reason: "Requires missing environment variable: " + spec.MissingCredentialEnv,
			Observed: observed, Desired: desiredEntry, Spec: specPtr(spec), IsAuthorized: true,
		}, currentText
	}

	if EntryMatchesDesired(spec, current) {
		fp := desiredFP
		return MCPOperation{
			Target: configTarget, Action: "NOOP", Reason: "Already synchronized", Observed: observed, Desired: desiredEntry,
			Spec: specPtr(spec), ForceIdentity: targetKey, IsAuthorized: true,
			StateTransition: &StateTransition{StateKey: spec.StateKey(), Fingerprint: &fp},
		}, currentText
	}

	safeToUpdate := current == nil || isAuthorized || (managedFP != nil && currentFP != nil && *managedFP == *currentFP)
	if !safeToUpdate {
		return MCPOperation{
			Target: configTarget, Action: "CONFLICT",
			Reason:   "Existing config was not last written by aikito; review it or rerun with --force",
			Observed: observed, Desired: desiredEntry, Spec: specPtr(spec), RequiresForce: true, ForceIdentity: targetKey, IsAuthorized: false,
		}, currentText
	}

	action := "UPDATE"
	reason := "Configuration updated"
	if current == nil {
		action = "CREATE"
		reason = "New server entry"
	}
	requiresForce := current != nil && (managedFP == nil || currentFP == nil || *managedFP != *currentFP)
	fp := desiredFP
	newText, err := UpdateEntry(spec, currentText)
	if err != nil {
		return MCPOperation{Target: configTarget, Action: "ERROR", Reason: "Failed to update entry: " + err.Error(), Spec: specPtr(spec), IsAuthorized: false}, currentText
	}
	return MCPOperation{
		Target: configTarget, Action: action, Reason: reason, Observed: observed, Desired: desiredEntry,
		Spec: specPtr(spec), RequiresForce: requiresForce, ForceIdentity: targetKey, IsAuthorized: true,
		StateTransition: &StateTransition{StateKey: spec.StateKey(), Fingerprint: &fp},
	}, newText
}

func managedFingerprint(state MCPState, stateKey string) *string {
	entry, ok := state.Entries[stateKey]
	if !ok || entry.Fingerprint == "" {
		return nil
	}
	fp := entry.Fingerprint
	return &fp
}

func specPtr(s AgentSpec) *AgentSpec { return &s }

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func readFileString(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// MapOperationToStatus mirrors _map_operation_to_status: the user-facing
// inspection status used by status/diff/doctor.
func MapOperationToStatus(op MCPOperation) string {
	switch op.Action {
	case "NOOP":
		return "OK"
	case "CREATE":
		return "MISSING"
	case "UPDATE":
		return "UPDATE"
	case "CONFLICT":
		return "DRIFT"
	case "SKIP":
		if op.Spec != nil && op.Spec.MissingCredentialEnv != "" {
			if op.Observed != nil && op.Observed.Exists && op.Observed.RawEntry() != nil {
				if EntryMatchesDesired(*op.Spec, op.Observed.RawEntry()) {
					return "OK"
				}
				return "DRIFT"
			}
		}
		return "SKIP"
	case "ERROR":
		return "ERROR"
	default:
		return op.Action
	}
}

// EvaluateSpecStatus mirrors evaluate_spec_status: if plan is given, scan it
// for the matching (agent, server) operation; otherwise build a throwaway
// one-spec plan. Any error during that throwaway build is swallowed to the
// soft "ERROR" string (this is a best-effort status probe, not the main
// sync path).
func EvaluateSpecStatus(spec AgentSpec, home string, plan *MCPPlan) string {
	if plan != nil {
		for _, op := range plan.Operations {
			if op.Target.Agent == spec.Agent && op.Target.LogicalIdentity == spec.Server {
				return MapOperationToStatus(op)
			}
		}
	}

	effectiveHome := home
	if effectiveHome == "" {
		effectiveHome = spec.Home
	}
	if effectiveHome == "" {
		if h, err := os.UserHomeDir(); err == nil {
			effectiveHome = h
		}
	}

	singlePlan, err := BuildMCPPlan(effectiveHome, effectiveHome, BuildMCPPlanOptions{Specs: []AgentSpec{spec}})
	if err != nil {
		return "ERROR"
	}
	if len(singlePlan.Operations) > 0 {
		return MapOperationToStatus(singlePlan.Operations[0])
	}
	return "SKIP"
}
