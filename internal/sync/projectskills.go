// Project-scoped resource synchronization: skills (honoring each project's
// own sync_mode, "link" or "copy"), plus project memory and instructions
// (always linked, regardless of sync_mode — matching architecture.md's
// documented design: "project instructions and memory always remain
// linked to the Aikito workspace... Selecting copy therefore does not make
// every managed project resource independent of Aikito").
//
// Deliberately NOT a full port of project_sync.py's actual dependency
// chain (skill_plan.py + skill_runtime.py + skill_state.py — ~3,300 lines
// total of CAS/revision-tracked state machine, built around a
// ConfigTarget/ConfigOperation framework this Go port hasn't ported). That
// subsystem turned out to be far larger than "the project-scoped version
// of global skill sync" its own design docs suggest; porting it in full
// is out of proportion for this task. This file instead reuses link.go's
// PlanSymlink/ApplySymlink for "link" mode (identical mechanism, just
// scoped to one project checkout instead of every configured agent) and
// adds a deliberately simple local state file for "copy" mode's drift
// detection, tracking only the last-synced canonical and copy tree
// digests per (project, skill) — enough to distinguish "still pristine,
// safe to refresh" from "modified since last sync" (CONFLICT), without
// Python's full CAS/revision/offline-checkout machinery.
package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mr-miles/aikito-rs/internal/registry"
	"github.com/mr-miles/aikito-rs/internal/workspace"
)

// ProjectSkillStateFile is home-relative, mirroring the shape (if not the
// exact schema) of internal/mcp's own state.go for a "what did we last
// write" local record.
const ProjectSkillStateFile = ".local/state/aikito/project-skill-state.json"

type projectSkillStateEntry struct {
	CanonicalFingerprint string `json:"canonical_fingerprint"`
	CopyFingerprint      string `json:"copy_fingerprint"`
}

type projectSkillStateFile struct {
	Version int                               `json:"version"`
	Entries map[string]projectSkillStateEntry `json:"entries"`
}

func loadProjectSkillState(home string) (projectSkillStateFile, error) {
	path := filepath.Join(home, ProjectSkillStateFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return projectSkillStateFile{Version: 1, Entries: map[string]projectSkillStateEntry{}}, nil
	}
	if err != nil {
		return projectSkillStateFile{}, err
	}
	var s projectSkillStateFile
	if err := json.Unmarshal(data, &s); err != nil {
		return projectSkillStateFile{}, fmt.Errorf("invalid project-skill state file %s: %w", path, err)
	}
	if s.Entries == nil {
		s.Entries = map[string]projectSkillStateEntry{}
	}
	return s, nil
}

func saveProjectSkillState(home string, s projectSkillStateFile) error {
	path := filepath.Join(home, ProjectSkillStateFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// ProjectSkillOperation is one planned skill-materialization operation for
// a single project checkout, in either "link" or "copy" mode.
type ProjectSkillOperation struct {
	Skill         string
	Mode          string // "link" | "copy"
	Action        string // "CREATE" | "UPDATE" | "NOOP" | "CONFLICT"
	TargetPath    string
	CanonicalPath string
	Reason        string
	RequiresForce bool
	IsAuthorized  bool
}

// BuildProjectSkillsPlan plans skill materialization for one project
// checkout's selected skills into <checkout>/.agents/skills/<name>.
func BuildProjectSkillsPlan(aikitoDir, home, projectName, checkoutPath string, selectedSkills []string, syncMode string, force bool) ([]ProjectSkillOperation, error) {
	var ops []ProjectSkillOperation
	agentsSkillsDir := filepath.Join(checkoutPath, ".agents", "skills")

	var state projectSkillStateFile
	if syncMode == "copy" {
		var err error
		state, err = loadProjectSkillState(home)
		if err != nil {
			return nil, err
		}
	}

	for _, skill := range selectedSkills {
		canonical := filepath.Join(aikitoDir, "skills", skill)
		target := filepath.Join(agentsSkillsDir, skill)

		if syncMode != "copy" {
			linkOp, err := PlanSymlink(target, canonical, "", skill, force)
			if err != nil {
				return nil, err
			}
			ops = append(ops, ProjectSkillOperation{
				Skill: skill, Mode: "link", Action: string(linkOp.Action),
				TargetPath: linkOp.TargetPath, CanonicalPath: linkOp.CanonicalPath,
				Reason: linkOp.Reason, RequiresForce: linkOp.RequiresForce, IsAuthorized: linkOp.IsAuthorized,
			})
			continue
		}

		op := ProjectSkillOperation{Skill: skill, Mode: "copy", TargetPath: target, CanonicalPath: canonical}

		canonicalFP, cerr := workspace.TreeDigest(canonical)
		if cerr != nil {
			op.Action = "CONFLICT"
			op.Reason = fmt.Sprintf("Canonical skill source missing or unreadable: %v", cerr)
			ops = append(ops, op)
			continue
		}

		key := projectName + "/" + skill
		rec, hasState := state.Entries[key]
		info, statErr := os.Lstat(target)

		switch {
		case os.IsNotExist(statErr):
			op.Action = "CREATE"
			op.Reason = "New copy"
			op.IsAuthorized = true
		case statErr != nil:
			return nil, statErr
		case info.Mode()&os.ModeSymlink != 0:
			op.Action = "CONFLICT"
			op.Reason = "Target is a symlink, not a managed copy; rerun with --force to replace it"
		default:
			copyFP, terr := workspace.TreeDigest(target)
			if terr != nil {
				return nil, terr
			}
			switch {
			case !hasState || copyFP != rec.CopyFingerprint:
				op.Action = "CONFLICT"
				op.Reason = fmt.Sprintf("Copy at %s has drifted from what aikito last wrote (hand-edited, or never synced before); rerun with --force to overwrite it", target)
			case canonicalFP != rec.CanonicalFingerprint:
				op.Action = "UPDATE"
				op.Reason = "Canonical skill changed since last sync"
				op.IsAuthorized = true
			default:
				op.Action = "NOOP"
				op.Reason = "Already synchronized"
				op.IsAuthorized = true
			}
		}

		if op.Action == "CONFLICT" && force {
			op.RequiresForce = true
			op.IsAuthorized = true
			if os.IsNotExist(statErr) {
				op.Action = "CREATE"
			} else {
				op.Action = "UPDATE"
			}
			op.Reason += " (forced)"
		}

		ops = append(ops, op)
	}
	return ops, nil
}

// ApplyProjectSkillOperation materializes op (CREATE/UPDATE only; NOOP/
// CONFLICT are no-ops here).
func ApplyProjectSkillOperation(home, projectName string, op ProjectSkillOperation) error {
	if op.Action != "CREATE" && op.Action != "UPDATE" {
		return nil
	}
	if op.Mode == "link" {
		return ApplySymlink(LinkOperation{Action: LinkCreate, TargetPath: op.TargetPath, CanonicalPath: op.CanonicalPath})
	}

	if _, err := os.Lstat(op.TargetPath); err == nil {
		if err := os.RemoveAll(op.TargetPath); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(op.TargetPath), 0o777); err != nil {
		return err
	}
	if err := copyPath(op.CanonicalPath, op.TargetPath); err != nil {
		return err
	}
	canonicalFP, err := workspace.TreeDigest(op.CanonicalPath)
	if err != nil {
		return err
	}
	state, err := loadProjectSkillState(home)
	if err != nil {
		return err
	}
	state.Version = 1
	state.Entries[projectName+"/"+op.Skill] = projectSkillStateEntry{
		CanonicalFingerprint: canonicalFP, CopyFingerprint: canonicalFP,
	}
	return saveProjectSkillState(home, state)
}

// BuildProjectInstructionsPlan plans one symlink per configured agent's
// ProjectInstructionPath (project-relative, per registry.Agent) pointing at
// the project's canonical projects/<name>/AGENTS.md — always linked
// regardless of the project's sync_mode (instructions.py's project
// instruction behavior; only project SKILLS honor sync_mode).
func BuildProjectInstructionsPlan(aikitoDir, projectName, checkoutPath string, reg *registry.AgentRegistry, force bool) ([]LinkOperation, error) {
	canonical := filepath.Join(aikitoDir, "projects", projectName, "AGENTS.md")
	if _, err := os.Stat(canonical); err != nil {
		return nil, fmt.Errorf("project instructions not found: %s", canonical)
	}
	var ops []LinkOperation
	for _, agent := range reg.Values() {
		if agent.ProjectInstructionPath == nil || *agent.ProjectInstructionPath == "" {
			continue
		}
		target := filepath.Join(checkoutPath, filepath.FromSlash(*agent.ProjectInstructionPath))
		op, err := PlanSymlink(target, canonical, agent.Name, "", force)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// BuildProjectMemoryPlan plans one symlink per canonical project memory
// note (projects/<name>/memory/**/*.md, both flat and notes/-nested) into
// <checkout>/.agents/memory/<relpath> — always linked regardless of
// sync_mode (memory_runtime.py's project memory behavior).
func BuildProjectMemoryPlan(aikitoDir, projectName, checkoutPath string, force bool) ([]LinkOperation, error) {
	memDir := filepath.Join(aikitoDir, "projects", projectName, "memory")
	var ops []LinkOperation
	walkErr := filepath.Walk(memDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") || workspace.IsIgnoredName(info.Name()) {
			return nil
		}
		rel, rerr := filepath.Rel(memDir, path)
		if rerr != nil {
			return nil
		}
		target := filepath.Join(checkoutPath, ".agents", "memory", rel)
		op, perr := PlanSymlink(target, path, "", filepath.ToSlash(rel), force)
		if perr != nil {
			return perr
		}
		ops = append(ops, op)
		return nil
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		return nil, walkErr
	}
	return ops, nil
}
