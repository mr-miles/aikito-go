package linkplan

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mr-miles/aikito-go/internal/compat"
	"github.com/mr-miles/aikito-go/internal/registry"
)

// InstructionBatch ports instructions.py's InstructionBatch.
type InstructionBatch struct {
	Scope            string // "global" or "project"
	CanonicalSource  string
	Targets          []registry.Target
	Enabled          bool
	StaleTargets     []registry.Target
	Checkout         string
	ProjectName      string
	OfflineCheckouts []string
}

// InstructionPlan ports InstructionPlan.
type InstructionPlan struct {
	Batch      InstructionBatch
	Operations []Operation
}

// Conflicts returns the CONFLICT operations.
func (p InstructionPlan) Conflicts() []Operation { return conflicts(p.Operations) }

// CanApply is InstructionPlan.can_apply.
func (p InstructionPlan) CanApply() bool { return canApply(p.Operations) }

func conflicts(ops []Operation) []Operation {
	var out []Operation
	for _, op := range ops {
		if op.Action == ActConflict {
			out = append(out, op)
		}
	}
	return out
}

func canApply(ops []Operation) bool {
	for _, op := range ops {
		if op.Action == ActConflict || !op.IsAuthorized {
			return false
		}
	}
	return true
}

// BuildGlobalInstructionBatch ports build_global_instruction_batch.
func BuildGlobalInstructionBatch(aikitoDir, home string, reg *registry.AgentRegistry) (InstructionBatch, error) {
	canonical := filepath.Join(aikitoDir, "global", "AGENTS.md")
	targets, err := registry.ResolveTargets("global_instructions", aikitoDir, home, reg, registry.ResolveTargetsOptions{})
	if err != nil {
		return InstructionBatch{}, err
	}
	var stale []registry.Target
	if reg.Contains("grok") {
		legacy := filepath.Join(home, ".grok", "AGENTS.md")
		formal := false
		for _, t := range targets {
			if IsSameTargetLocation(t.Path, legacy) {
				formal = true
			}
		}
		if !formal && (isSymlink(legacy) || exists(legacy)) {
			stale = append(stale, registry.Target{
				Kind: InstructionLink, Scope: "global", Path: legacy, CanonicalSource: canonical,
				Consumers: []string{"grok"}, ConsumerDisplayNames: []string{"Grok (legacy)"},
			})
		}
	}
	return InstructionBatch{Scope: "global", CanonicalSource: canonical, Targets: targets, Enabled: true, StaleTargets: stale}, nil
}

func displayName(t registry.Target) string {
	if n := strings.Join(t.ConsumerDisplayNames, "/"); n != "" {
		return n
	}
	return filepath.Base(t.Path)
}

func isRelativeTo(p, base string) bool {
	rel, err := filepath.Rel(base, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// PlanInstructions ports plan_instructions (both scopes).
func PlanInstructions(batch InstructionBatch, home string, isOffline bool) InstructionPlan {
	var ops []Operation
	canonical := batch.CanonicalSource
	canonicalValid := isFile(canonical)
	canonicalError := ""
	if batch.Scope == "global" && !canonicalValid {
		canonicalError = fmt.Sprintf("Global instruction file not found: %s", canonical)
	}
	offlineSkip := func(t registry.Target) Operation {
		return Operation{
			Action: ActSkip, RuleID: "INV-INST-11", TargetPath: t.Path, CanonicalPath: canonical,
			Reason: fmt.Sprintf("Checkout is offline: %s", t.Path), ExpectedRepresentation: "missing",
			DesiredRepresentation: "link", TargetKind: InstructionLink, ResourceName: displayName(t), IsAuthorized: true,
		}
	}
	if isOffline {
		for _, t := range batch.Targets {
			ops = append(ops, offlineSkip(t))
		}
		return InstructionPlan{Batch: batch, Operations: ops}
	}

	desired := "absent"
	if batch.Enabled {
		desired = "link"
	}
	for _, target := range batch.Targets {
		offline := false
		for _, off := range batch.OfflineCheckouts {
			if target.Path == off || isRelativeTo(target.Path, off) {
				offline = true
			}
		}
		if offline {
			ops = append(ops, offlineSkip(target))
			continue
		}
		sameObj := target.IsSameObject() && !isSymlink(target.Path)
		observed := Inspect(target.Path, canonical, InspectOptions{
			CanonicalInvalid: !canonicalValid, CanonicalError: canonicalError,
			TargetKind: InstructionLink, Scope: batch.Scope, IsSameObject: sameObj,
		})
		avail := registry.CheckTargetAvailability(target, home)
		parentExists := exists(filepath.Dir(target.Path))
		name := displayName(target)
		op := Plan(observed, desired, PlanOptions{AvailabilityStatus: avail.Status, ParentExists: &parentExists, ResourceName: name})

		mk := func(action, rule, reason, finding, expected, desired string, authorized bool) Operation {
			return Operation{
				Action: action, RuleID: rule, TargetPath: target.Path, CanonicalPath: canonical,
				Reason: reason, Finding: finding, ExpectedRepresentation: expected, DesiredRepresentation: desired,
				TargetKind: InstructionLink, ResourceName: name, IsAuthorized: authorized,
			}
		}
		if batch.Enabled {
			switch {
			case observed.IsSameObject:
				op = mk(ActSharedPath, "INV-INST-05",
					fmt.Sprintf("Target path is the same physical object as canonical instructions; no link required for '%s'", name),
					"", observed.EntryType, "link", true)
				op.IsSameObject = true
			case !canonicalValid:
				op = mk(ActConflict, "INV-TR-02",
					fmt.Sprintf("Canonical instruction file not found: %s", canonical),
					fmt.Sprintf("Missing instruction source: %s", canonical), observed.EntryType, "link", false)
			case observed.EntryType == EntryFile:
				op = mk(ActConflict, "INV-INST-03",
					fmt.Sprintf("Target preserved: %s. Target is a regular file; Aikito does not overwrite existing instruction files. "+
						"Move or remove it manually, then run sync again.", target.Path),
					fmt.Sprintf("Pre-existing regular instruction file: %s", target.Path), "file", "link", false)
			case observed.EntryType == EntrySymlink:
				if observed.LinkPointsToCanonical {
					op = mk(ActNoop, "INV-INST-12",
						fmt.Sprintf("Symbolic link already points to canonical instructions for '%s'", name), "", "symlink", "link", true)
				} else {
					rule := "INV-INST-02"
					if batch.Scope == "global" {
						rule = "INV-INST-06"
					}
					dest := observed.dest()
					op = mk(ActConflict, rule,
						fmt.Sprintf("Target preserved: %s. Symbolic link points to unauthorized destination: %s (expected %s). "+
							"Foreign or external instruction symlinks will not be overwritten automatically; "+
							"inspect manually, then run sync again.", target.Path, dest, canonical),
						fmt.Sprintf("Unauthorized instruction symlink destination: %s -> %s", target.Path, dest),
						"symlink", "link", false)
				}
			case observed.EntryType == EntryMissing && op.Action == ActCreate:
				rule := "INV-TR-01"
				if batch.Scope == "project" {
					rule = "INV-INST-07"
				}
				requires := op.RequiresParentCreation
				op = mk(ActCreate, rule, fmt.Sprintf("Create symbolic link: %s -> %s", target.Path, canonical), "", "missing", "link", true)
				op.RequiresParentCreation = requires
			}
		} else {
			switch {
			case observed.IsSameObject:
				op = mk(ActNoop, "INV-INST-05",
					fmt.Sprintf("Canonical instructions file itself is not deleted: %s", target.Path), "", observed.EntryType, "absent", true)
				op.IsSameObject = true
			case observed.EntryType == EntrySymlink:
				if observed.LinkPointsToCanonical {
					op = mk(ActUnlink, "INV-INST-08",
						fmt.Sprintf("Remove unselected instruction symbolic link: %s", target.Path), "", "symlink", "absent", true)
				} else {
					op = mk(ActNoop, "INV-INST-08",
						fmt.Sprintf("Preserve foreign or unmanaged symbolic link: %s -> %s", target.Path, observed.dest()),
						fmt.Sprintf("Unmanaged instruction symlink: %s", target.Path), "symlink", "absent", true)
				}
			case observed.EntryType == EntryFile:
				op = mk(ActNoop, "INV-INST-10",
					fmt.Sprintf("Preserve project-owned instruction file: %s", target.Path),
					fmt.Sprintf("Project-owned instruction file preserved: %s", target.Path), "file", "absent", true)
			case observed.EntryType == EntryMissing:
				op = mk(ActNoop, "INV-INST-12",
					fmt.Sprintf("Instruction link is already absent: %s", target.Path), "", "missing", "absent", true)
			}
		}
		ops = append(ops, op)
	}

	for _, st := range batch.StaleTargets {
		observed := Inspect(st.Path, canonical, InspectOptions{
			CanonicalInvalid: !canonicalValid, TargetKind: InstructionLink, Scope: batch.Scope,
		})
		mk := func(action, reason, finding, expected string) Operation {
			return Operation{
				Action: action, RuleID: "INV-INST-09", TargetPath: st.Path, CanonicalPath: canonical,
				Reason: reason, Finding: finding, ExpectedRepresentation: expected, DesiredRepresentation: "absent",
				TargetKind: InstructionLink, ResourceName: displayName(st), IsAuthorized: true,
			}
		}
		var op Operation
		switch observed.EntryType {
		case EntrySymlink:
			if observed.LinkPointsToCanonical {
				op = mk(ActUnlink, fmt.Sprintf("Remove legacy instruction link: %s", st.Path), "", "symlink")
			} else {
				op = mk(ActNoop, fmt.Sprintf("Preserve unmanaged legacy item: %s", st.Path),
					fmt.Sprintf("Unmanaged legacy instruction item: %s", st.Path), "symlink")
			}
		case EntryMissing:
			op = mk(ActNoop, fmt.Sprintf("Legacy instruction entry is already absent: %s", st.Path), "", "missing")
		default:
			op = mk(ActNoop, fmt.Sprintf("Preserve unmanaged legacy item of type %s: %s", observed.EntryType, st.Path),
				fmt.Sprintf("Unmanaged legacy instruction entry: %s", st.Path), observed.EntryType)
		}
		ops = append(ops, op)
	}
	return InstructionPlan{Batch: batch, Operations: ops}
}

// InstructionExecutionResult ports InstructionExecutionResult.
type InstructionExecutionResult struct {
	Success      bool
	Applied      int
	ErrorMessage string
}

func readStripped(p string) (string, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(compat.DecodeUTF8Replace(data)), nil
}

// ExecuteInstructionPlan ports execute_instruction_plan.
func ExecuteInstructionPlan(plan InstructionPlan, dryRun, verbose bool, stdout, stderr io.Writer) InstructionExecutionResult {
	if !plan.CanApply() && !dryRun {
		return InstructionExecutionResult{ErrorMessage: "Instruction plan contains unresolved conflicts."}
	}
	canonical := plan.Batch.CanonicalSource
	if plan.Batch.Enabled {
		if !isFile(canonical) {
			return InstructionExecutionResult{ErrorMessage: fmt.Sprintf(
				"Preflight failed: canonical instruction file not found: %s (stale plan)", canonical)}
		}
		if plan.Batch.Scope == "project" {
			content, err := readStripped(canonical)
			if err != nil {
				return InstructionExecutionResult{ErrorMessage: fmt.Sprintf(
					"Preflight failed: could not read canonical instruction file: %v", err)}
			}
			if content == "" {
				return InstructionExecutionResult{ErrorMessage: fmt.Sprintf(
					"Preflight failed: canonical instruction file changed from non-empty to empty: %s (stale plan)", canonical)}
			}
		}
	} else if isFile(canonical) {
		content, err := readStripped(canonical)
		if err != nil {
			return InstructionExecutionResult{ErrorMessage: fmt.Sprintf(
				"Preflight failed: could not read canonical instruction file: %v", err)}
		}
		if content != "" {
			return InstructionExecutionResult{ErrorMessage: fmt.Sprintf(
				"Preflight failed: canonical instruction file changed from empty to non-empty: %s (stale plan)", canonical)}
		}
	}
	res := InstructionExecutionResult{Success: true}
	for _, op := range plan.Operations {
		r := Apply(op, dryRun, verbose, stdout, stderr)
		if !r.Success {
			msg := r.ErrorMessage
			if msg == "" {
				msg = fmt.Sprintf("Failed operation on %s", op.TargetPath)
			}
			return InstructionExecutionResult{Applied: res.Applied, ErrorMessage: msg}
		}
		if op.Action != ActSharedPath && op.Action != ActSkip && op.Action != ActNoop && r.Applied {
			res.Applied++
		}
	}
	return res
}
