package projectsync

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mr-miles/aikito-go/internal/linkplan"
	"github.com/mr-miles/aikito-go/internal/workspace"
	"github.com/mr-miles/aikito-go/internal/writerlock"
)

// SkillExecutionResult is skill_runtime.py's SkillExecutionResult.
type SkillExecutionResult struct {
	AppliedOps       []SkillOperation
	SkippedOps       []SkillOperation
	FailedOps        []SkillOperation
	RecoveryRequired bool
	ContentChanges   int
	StateOnlyChanges int
	ErrorMessage     string
}

func (r SkillExecutionResult) IsSuccess() bool {
	return len(r.FailedOps) == 0 && !r.RecoveryRequired && r.ErrorMessage == ""
}

// validateCanonicalSkillSource is validate_canonical_skill_source. The
// returned path is "" for None.
func validateCanonicalSkillSource(workspaceRoot, skillName string) (string, string) {
	skillsDir := filepath.Join(workspaceRoot, "skills")
	if !exists(skillsDir) {
		return "", fmt.Sprintf("Workspace skills directory missing: %s", skillsDir)
	}
	if isReparsePoint(skillsDir) {
		return "", fmt.Sprintf("Workspace skills root cannot be a symlink or reparse point: %s", skillsDir)
	}
	canonical := filepath.Join(skillsDir, skillName)
	if !exists(canonical) {
		return canonical, fmt.Sprintf("Canonical skill source does not exist: %s", canonical)
	}
	if isReparsePoint(canonical) {
		return canonical, fmt.Sprintf("Canonical skill directory cannot be a symlink or reparse point: %s", canonical)
	}
	if !isDir(canonical) {
		return canonical, fmt.Sprintf("Canonical skill source is not a directory: %s", canonical)
	}
	resolvedWS, err1 := filepath.EvalSymlinks(workspaceRoot)
	resolvedCanon, err2 := filepath.EvalSymlinks(canonical)
	if err1 != nil || err2 != nil {
		err := err1
		if err == nil {
			err = err2
		}
		return canonical, fmt.Sprintf("Boundary resolution failed for %s: %s", canonical, pyOSError(err))
	}
	if !isRelativeTo(resolvedCanon, resolvedWS) {
		return canonical, fmt.Sprintf("Canonical skill path escapes workspace boundary: %s -> %s", canonical, resolvedCanon)
	}
	return canonical, ""
}

// InspectSkillTarget is inspect_skill_target.
func InspectSkillTarget(t SkillTarget, desiredMode, home string) (ObservedSkill, DesiredSkill) {
	canonicalPath, canonicalErr := validateCanonicalSkillSource(t.WorkspaceRoot, t.SkillName)
	canonicalValid := canonicalErr == ""
	canonicalFP := ""
	if canonicalValid && canonicalPath != "" && isDir(canonicalPath) {
		fp, fpErr := CalculateDirectoryFingerprint(canonicalPath)
		if fpErr != "" {
			canonicalValid = false
			canonicalErr = fpErr
		} else {
			canonicalFP = fp
		}
	}

	o := ObservedSkill{Target: t, EntryType: "missing", CanonicalValid: canonicalValid, CanonicalError: canonicalErr, CanonicalFingerprint: canonicalFP}
	runtimePath := t.TargetPath
	if isSymlink(runtimePath) {
		o.EntryType = "symlink"
		o.ResolvedLinkTarget = resolveSymlinkTarget(runtimePath)
		if raw, err := os.Readlink(runtimePath); err == nil {
			o.RawLinkTarget = linkplan.PathlibJoin(filepath.Dir(runtimePath), raw)
		}
		skillsRoot := resolve(filepath.Join(t.WorkspaceRoot, "skills"))
		for _, cand := range []string{o.ResolvedLinkTarget, o.RawLinkTarget} {
			if cand != "" && isRelativeTo(resolve(cand), skillsRoot) {
				o.LinkPointsWithinCanonical = true
				break
			}
		}
		if canonicalPath != "" {
			expected := resolve(canonicalPath)
			if o.ResolvedLinkTarget != "" && resolve(o.ResolvedLinkTarget) == expected {
				o.LinkPointsToCanonical = true
			} else if o.RawLinkTarget != "" && resolve(o.RawLinkTarget) == expected {
				o.LinkPointsToCanonical = true
			}
		}
	} else if exists(runtimePath) {
		if isDir(runtimePath) {
			o.EntryType = "dir"
			o.RuntimeFingerprint, _ = CalculateDirectoryFingerprint(runtimePath)
		} else {
			o.EntryType = "unsupported"
		}
	}

	doc, stateErr := LoadProjectSkillState(home, t.WorkspaceRoot, t.ProjectName, t.PhysicalCheckout)
	o.StateError = stateErr
	if doc != nil {
		o.StateRevision = doc.Revision
		if rec, ok := doc.Records[t.SkillName]; ok {
			r := rec
			o.StateRecord = &r
		}
	}
	d := DesiredSkill{SkillName: t.SkillName, Mode: desiredMode, CanonicalPath: canonicalPath, CanonicalFingerprint: canonicalFP}
	if d.CanonicalPath == "" {
		d.CanonicalPath = filepath.Join(t.WorkspaceRoot, "skills", t.SkillName)
	}
	return o, d
}

func liveRepresentation(p string) string {
	switch {
	case isSymlink(p):
		return "link"
	case !exists(p):
		return "missing"
	case isDir(p):
		return "copy"
	}
	return "unsupported"
}

func newTxID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b[:])
}

// copyTree is shutil.copytree(src, dst, ignore=ignore_patterns(metadata))
// with copy2 semantics (contents, permission bits and timestamps).
func copyTree(src, dst string) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o777); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == workspace.SkillExecutableMetadataFilename {
			continue
		}
		s, d := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		if isDir(s) {
			if err := copyTree(s, d); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(s, d); err != nil {
			return err
		}
	}
	if err := os.Chmod(dst, st.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(dst, st.ModTime(), st.ModTime())
}

func copyFile(src, dst string) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Chmod(dst, st.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(dst, st.ModTime(), st.ModTime())
}

// ExecuteSkillPlan is execute_skill_plan.
func ExecuteSkillPlan(out Out, plan SkillPlan, home string, dryRun bool) SkillExecutionResult {
	if !plan.CanApply {
		return SkillExecutionResult{FailedOps: plan.Operations, ErrorMessage: "Plan contains authorization or preflight errors; cannot apply."}
	}
	if dryRun {
		res := SkillExecutionResult{AppliedOps: plan.Operations}
		for _, op := range plan.Operations {
			switch op.Action {
			case "CREATE", "UPDATE", "UNLINK":
				res.ContentChanges++
			case "RECONCILE_STATE", "CLAIM_STATE", "REACTIVATE_STATE":
				res.StateOnlyChanges++
			}
		}
		return res
	}

	lock, err := writerlock.Acquire(home)
	if err != nil {
		return SkillExecutionResult{FailedOps: plan.Operations, ErrorMessage: err.Error()}
	}
	defer lock.Release()

	var skillNames []string
	for _, op := range plan.Operations {
		skillNames = append(skillNames, op.Target.SkillName)
	}
	recovered, recMsg := RunRecoveryPass(home, plan.WorkspaceRoot, []string{plan.ProjectName}, skillNames, nil)
	if recovered {
		return SkillExecutionResult{RecoveryRequired: true, ErrorMessage: fmt.Sprintf("Pending transaction recovered: %s. Please re-run command.", recMsg)}
	}
	if recMsg != "" {
		return SkillExecutionResult{RecoveryRequired: true, ErrorMessage: "Recovery failed: " + recMsg}
	}

	// Group by checkout, preserving first-seen order (dict insertion order).
	var checkouts []string
	opsByCheckout := map[string][]SkillOperation{}
	for _, op := range plan.Operations {
		co := op.Target.PhysicalCheckout
		if _, ok := opsByCheckout[co]; !ok {
			checkouts = append(checkouts, co)
		}
		opsByCheckout[co] = append(opsByCheckout[co], op)
	}

	initialRaw := map[string]*ProjectSkillStateDocument{}
	initial := map[string]*ProjectSkillStateDocument{}
	for _, co := range checkouts {
		doc, loadErr := LoadProjectSkillState(home, plan.WorkspaceRoot, plan.ProjectName, co)
		if loadErr != "" {
			return SkillExecutionResult{FailedOps: plan.Operations, ErrorMessage: "State loading error: " + loadErr}
		}
		initialRaw[co] = doc
		if doc == nil {
			doc = &ProjectSkillStateDocument{Version: 1, WorkspaceRoot: asPosix(plan.WorkspaceRoot), ProjectName: plan.ProjectName,
				PhysicalCheckout: asPosix(co), Records: map[string]SkillStateRecord{}}
		}
		initial[co] = doc
	}

	cas := plan.ConfigCAS
	casActive := cas != nil && !cas.IsNoop
	if casActive {
		live, err := os.ReadFile(cas.ConfigPath)
		if err != nil {
			return SkillExecutionResult{FailedOps: plan.Operations, ErrorMessage: "Failed to read project configuration for CAS verification: " + pyOSError(err)}
		}
		if string(normalizeBytes(live)) != string(normalizeBytes(cas.PreImage)) {
			return SkillExecutionResult{FailedOps: plan.Operations, ErrorMessage: fmt.Sprintf(
				"Concurrent configuration modification detected in %s. Plan invalidated; please re-run.", cas.ConfigPath)}
		}
	}

	fail1 := func(op SkillOperation, msg string) SkillExecutionResult {
		return SkillExecutionResult{FailedOps: []SkillOperation{op}, ErrorMessage: msg}
	}
	for _, co := range checkouts {
		rev := initial[co].Revision
		for _, op := range opsByCheckout[co] {
			if op.Action == "NOOP" {
				continue
			}
			if op.ExpectedRevision != nil && rev != *op.ExpectedRevision {
				return fail1(op, fmt.Sprintf("State document changed since plan for skill '%s': expected revision %d, found %d. Plan invalidated; please re-run.",
					op.Target.SkillName, *op.ExpectedRevision, rev))
			}
			t := op.Target
			canonicalSource := filepath.Join(plan.WorkspaceRoot, "skills", t.SkillName)
			live := liveRepresentation(t.TargetPath)
			exp := op.ExpectedRepresentation
			matches := (exp == "missing" && live == "missing") ||
				((exp == "link" || exp == "symlink") && live == "link") ||
				((exp == "copy" || exp == "dir") && live == "copy") ||
				((exp == "unsupported" || exp == "file") && live == "unsupported")
			if !matches {
				return fail1(op, fmt.Sprintf("Target representation changed since plan for skill '%s': expected '%s', found '%s' at %s. Plan invalidated; please re-run to regenerate plan.",
					t.SkillName, exp, live, t.TargetPath))
			}
			if exp == "link" || exp == "symlink" {
				dest := physical(resolveSymlinkTarget(t.TargetPath))
				if !(dest == physical(canonicalSource) || isRelativeTo(dest, physical(filepath.Join(plan.WorkspaceRoot, "skills")))) {
					return fail1(op, fmt.Sprintf("Symbolic link destination changed since plan for skill '%s': %s no longer points to canonical source. Plan invalidated; please re-run to regenerate plan.",
						t.SkillName, t.TargetPath))
				}
			}
			if op.ExpectedFingerprint != "" {
				if live != "copy" {
					return fail1(op, fmt.Sprintf("Target entry for skill '%s' is no longer a directory. Plan invalidated; please re-run.", t.SkillName))
				}
				fp, fpErr := CalculateDirectoryFingerprint(t.TargetPath)
				if fpErr != "" || fp != op.ExpectedFingerprint {
					return fail1(op, fmt.Sprintf("Runtime content changed since plan for skill '%s': expected fingerprint %s, found %s. Plan invalidated; please re-run.",
						t.SkillName, pyRepr(op.ExpectedFingerprint), pyReprOpt(fp)))
				}
			}
			if op.Action == "CREATE" && op.DesiredRepresentation == "link" {
				if _, cErr := validateCanonicalSkillSource(plan.WorkspaceRoot, t.SkillName); cErr != "" {
					return fail1(op, fmt.Sprintf("Canonical skill source invalid for '%s': %s", t.SkillName, cErr))
				}
			}
		}
	}

	allNoop := true
	for _, op := range plan.Operations {
		if op.Action != "NOOP" {
			allNoop = false
		}
	}
	if allNoop && !casActive {
		return SkillExecutionResult{SkippedOps: plan.Operations}
	}

	var applied, skipped []SkillOperation
	contentChanges, stateOnly := 0, 0
	txID := newTxID()
	activeTxRoots := map[string]bool{}
	var txRootOrder []string
	configApplied := false

	var files []map[string]any
	if casActive {
		files = append(files, map[string]any{
			"path":              cas.ConfigPath,
			"pre_image_base64":  base64.StdEncoding.EncodeToString(cas.PreImage),
			"post_image_base64": base64.StdEncoding.EncodeToString(cas.PostImage),
		})
	}
	var symlinks, recoveryDirs []map[string]any
	type dirs struct{ staging, recovery string }
	opDirs := map[string]dirs{}
	key := func(co, skill string) string { return co + "\x00" + skill }
	addRoot := func(r string) {
		if !activeTxRoots[r] {
			activeTxRoots[r] = true
			txRootOrder = append(txRootOrder, r)
		}
	}
	for _, co := range checkouts {
		txRoot := filepath.Join(co, ".agents", ".aikito-tx", txID)
		for _, op := range opsByCheckout[co] {
			t := op.Target
			canonSource := filepath.Join(plan.WorkspaceRoot, "skills", t.SkillName)
			var origLink any
			if isSymlink(t.TargetPath) {
				origLink = resolveSymlinkTarget(t.TargetPath)
			}
			had := lexists(t.TargetPath)
			recField := func(p string) string {
				if had {
					return p
				}
				return ""
			}
			switch {
			case op.Action == "UNLINK":
				symlinks = append(symlinks, map[string]any{"target_path": t.TargetPath, "pre_link": origLink, "post_link": nil})
			case op.Action == "CREATE" && op.DesiredRepresentation == "link":
				symlinks = append(symlinks, map[string]any{"target_path": t.TargetPath, "pre_link": origLink, "post_link": canonSource})
			case (op.Action == "CREATE" || op.Action == "UPDATE") && op.DesiredRepresentation == "copy":
				staging := filepath.Join(txRoot, "stage-"+t.SkillName)
				recovery := filepath.Join(txRoot, "prev-"+t.SkillName)
				opDirs[key(co, t.SkillName)] = dirs{staging, recovery}
				addRoot(txRoot)
				recoveryDirs = append(recoveryDirs, map[string]any{
					"target_path": t.TargetPath, "recovery_dir": recField(recovery), "staging_dir": staging,
					"created": !had, "pre_fingerprint": optAny(op.ExpectedFingerprint), "post_fingerprint": optAny(op.DesiredFingerprint),
				})
			case op.Action == "UPDATE" && op.DesiredRepresentation == "link":
				recovery := filepath.Join(txRoot, "prev-"+t.SkillName)
				opDirs[key(co, t.SkillName)] = dirs{txRoot, recovery}
				addRoot(txRoot)
				recoveryDirs = append(recoveryDirs, map[string]any{
					"target_path": t.TargetPath, "recovery_dir": recField(recovery), "staging_dir": "",
					"created": false, "pre_fingerprint": optAny(op.ExpectedFingerprint), "post_fingerprint": nil,
					"expected_symlink_target": canonSource,
				})
			}
		}
	}
	var transitions []map[string]any
	for _, co := range checkouts {
		raw := initialRaw[co]
		var preDoc any
		preRev := 0
		if raw != nil {
			preDoc = raw.toDict()
			preRev = raw.Revision
		}
		transitions = append(transitions, map[string]any{
			"binding_hash": BindingHash(plan.WorkspaceRoot, plan.ProjectName, co), "workspace_root": asPosix(plan.WorkspaceRoot),
			"project_name": plan.ProjectName, "physical_checkout": asPosix(co),
			"pre_doc": preDoc, "pre_revision": preRev, "post_docs": []any{},
		})
	}
	affected := map[string]bool{}
	var affectedList []string
	for _, op := range plan.Operations {
		if op.Target.SkillName != "" && !affected[op.Target.SkillName] {
			affected[op.Target.SkillName] = true
			affectedList = append(affectedList, op.Target.SkillName)
		}
	}
	var coPosix []string
	for _, co := range checkouts {
		coPosix = append(coPosix, asPosix(co))
	}
	journal := &Journal{TxID: txID, Kind: "runtime_apply", Phase: "pending", WorkspaceRoot: asPosix(plan.WorkspaceRoot),
		ProjectNames: []string{plan.ProjectName}, CheckoutPaths: coPosix, AffectedSkills: affectedList,
		Files: files, RecoveryDirs: recoveryDirs, StateTransitions: transitions, Symlinks: symlinks}
	if jErr := writeJournal(home, journal); jErr != "" {
		return SkillExecutionResult{FailedOps: plan.Operations, ErrorMessage: "Cannot persist transaction journal: " + jErr}
	}

	persistPostImage := func(co string, doc *ProjectSkillStateDocument) string {
		anticipated := doc.toDict()
		anticipated["revision"] = doc.Revision + 1
		h := BindingHash(plan.WorkspaceRoot, plan.ProjectName, co)
		for _, tr := range journal.StateTransitions {
			if tr["binding_hash"] == h {
				tr["post_docs"] = append(tr["post_docs"].([]any), anticipated)
				break
			}
		}
		return writeJournal(home, journal)
	}
	checkpoint := func(op SkillOperation, co string, doc *ProjectSkillStateDocument) string {
		tp := op.Target.TargetPath
		var rd, sl []map[string]any
		for _, e := range journal.RecoveryDirs {
			if e["target_path"] != tp {
				rd = append(rd, e)
			}
		}
		for _, e := range journal.Symlinks {
			if e["target_path"] != tp {
				sl = append(sl, e)
			}
		}
		journal.RecoveryDirs, journal.Symlinks = rd, sl
		h := BindingHash(plan.WorkspaceRoot, plan.ProjectName, co)
		for _, tr := range journal.StateTransitions {
			if tr["binding_hash"] == h {
				if len(doc.Records) > 0 {
					tr["pre_doc"] = doc.toDict()
				} else {
					tr["pre_doc"] = nil
				}
				tr["pre_revision"] = doc.Revision
				tr["post_docs"] = []any{}
				break
			}
		}
		if configApplied {
			for _, fe := range journal.Files {
				fe["pre_image_base64"] = fe["post_image_base64"]
			}
		}
		return writeJournal(home, journal)
	}
	rollback := func(failed SkillOperation, msg string) SkillExecutionResult {
		var cos []string
		cos = append(cos, checkouts...)
		recovered, recErr := RunRecoveryPass(home, plan.WorkspaceRoot, []string{plan.ProjectName}, []string{failed.Target.SkillName}, cos)
		required := !recovered
		// As in Python, any message from the recovery pass (even the
		// "Rolled back ..." summary of a successful one) is reported.
		if recErr != "" {
			required = true
			msg = fmt.Sprintf("%s; recovery failed: %s", msg, recErr)
		}
		res := SkillExecutionResult{AppliedOps: applied, SkippedOps: skipped, RecoveryRequired: required,
			ContentChanges: contentChanges, StateOnlyChanges: stateOnly, ErrorMessage: msg}
		alreadyApplied := false
		for _, a := range applied {
			if a.Target == failed.Target && a.Action == failed.Action {
				alreadyApplied = true
			}
		}
		if !alreadyApplied {
			res.FailedOps = []SkillOperation{failed}
		}
		return res
	}

	if casActive {
		tmp := filepath.Join(filepath.Dir(cas.ConfigPath), "."+filepath.Base(cas.ConfigPath)+".cas_tmp")
		err := os.WriteFile(tmp, cas.PostImage, 0o666)
		if err == nil {
			err = os.Rename(tmp, cas.ConfigPath)
		}
		if err != nil {
			if exists(tmp) {
				os.Remove(tmp)
			}
			deleteJournal(home, txID)
			return SkillExecutionResult{FailedOps: plan.Operations, ErrorMessage: "Failed to atomically register candidate path: " + pyOSError(err)}
		}
		configApplied = true
	}

	working := map[string]*ProjectSkillStateDocument{}
	for co, d := range initial {
		working[co] = d.clone()
	}

	for _, co := range checkouts {
		doc := working[co]
		for _, op := range opsByCheckout[co] {
			t := op.Target
			canonicalSource := filepath.Join(plan.WorkspaceRoot, "skills", t.SkillName)
			switch {
			case op.Action == "NOOP":
				skipped = append(skipped, op)
				continue

			case op.Action == "UNLINK":
				if isSymlink(t.TargetPath) {
					if err := os.Remove(t.TargetPath); err != nil {
						return rollback(op, fmt.Sprintf("Failed to unlink %s: %s", t.TargetPath, pyOSError(err)))
					}
				}
				if e := checkpoint(op, co, doc); e != "" {
					return rollback(op, "Failed to checkpoint unlink: "+e)
				}
				contentChanges++
				applied = append(applied, op)

			case op.Action == "CREATE" && op.DesiredRepresentation == "link":
				if _, cErr := validateCanonicalSkillSource(plan.WorkspaceRoot, t.SkillName); cErr != "" {
					return rollback(op, fmt.Sprintf("Canonical skill source invalid for '%s': %s", t.SkillName, cErr))
				}
				os.MkdirAll(filepath.Dir(t.TargetPath), 0o777)
				if !safeSymlink(out, canonicalSource, t.TargetPath) {
					return rollback(op, "Failed to create symlink for "+t.SkillName)
				}
				if e := checkpoint(op, co, doc); e != "" {
					return rollback(op, "Failed to checkpoint symlink creation: "+e)
				}
				contentChanges++
				applied = append(applied, op)

			case (op.Action == "CREATE" || op.Action == "UPDATE") && op.DesiredRepresentation == "copy":
				dd := opDirs[key(co, t.SkillName)]
				if err := os.MkdirAll(filepath.Dir(dd.staging), 0o777); err == nil {
					if err := copyTree(canonicalSource, dd.staging); err != nil {
						return rollback(op, fmt.Sprintf("Failed to stage copy for skill '%s': %s", t.SkillName, pyOSError(err)))
					}
				} else {
					return rollback(op, fmt.Sprintf("Failed to stage copy for skill '%s': %s", t.SkillName, pyOSError(err)))
				}
				stageFP, stageErr := CalculateDirectoryFingerprint(dd.staging)
				canonFP, canonErr := CalculateDirectoryFingerprint(canonicalSource)
				if stageErr != "" || canonErr != "" || stageFP != op.DesiredFingerprint || canonFP != op.DesiredFingerprint {
					return rollback(op, fmt.Sprintf("Secondary canonical verification failed for skill '%s'. Source changed during staging; apply aborted.", t.SkillName))
				}
				prevRevision := doc.Revision
				if lexists(t.TargetPath) {
					if err := os.Rename(t.TargetPath, dd.recovery); err != nil {
						return rollback(op, fmt.Sprintf("Failed to replace target for skill '%s': %s", t.SkillName, pyOSError(err)))
					}
				}
				os.MkdirAll(filepath.Dir(t.TargetPath), 0o777)
				if err := os.Rename(dd.staging, t.TargetPath); err != nil {
					return rollback(op, fmt.Sprintf("Failed to replace target for skill '%s': %s", t.SkillName, pyOSError(err)))
				}
				baseline := op.DesiredFingerprint
				if baseline == "" {
					baseline = canonFP
				}
				doc.Records[t.SkillName] = SkillStateRecord{SkillName: t.SkillName, Representation: "copy",
					Lifecycle: orStr(op.NextStateLifecycle, "active"), BaselineFingerprint: baseline,
					BaselineOrigin: orStr(op.NextBaselineOrigin, "write"), LastObservedSelected: true}
				if e := persistPostImage(co, doc); e != "" {
					return rollback(op, fmt.Sprintf("Failed to journal state for skill '%s': %s", t.SkillName, e))
				}
				if ok, e := SaveProjectSkillState(home, doc, prevRevision); !ok {
					return rollback(op, fmt.Sprintf("Failed to commit state for skill '%s': %s", t.SkillName, e))
				}
				if e := checkpoint(op, co, doc); e != "" {
					return rollback(op, fmt.Sprintf("Failed to checkpoint skill '%s': %s", t.SkillName, e))
				}
				contentChanges++
				applied = append(applied, op)

			case op.Action == "RECONCILE_STATE" || op.Action == "CLAIM_STATE" || op.Action == "REACTIVATE_STATE":
				prevRevision := doc.Revision
				doc.Records[t.SkillName] = SkillStateRecord{SkillName: t.SkillName, Representation: "copy", Lifecycle: "active",
					BaselineFingerprint: op.DesiredFingerprint, BaselineOrigin: orStr(op.NextBaselineOrigin, "reconcile"), LastObservedSelected: true}
				if e := persistPostImage(co, doc); e != "" {
					return rollback(op, fmt.Sprintf("Failed to journal state transition for '%s': %s", t.SkillName, e))
				}
				if ok, e := SaveProjectSkillState(home, doc, prevRevision); !ok {
					return rollback(op, fmt.Sprintf("Failed to commit state transition for '%s': %s", t.SkillName, e))
				}
				if e := checkpoint(op, co, doc); e != "" {
					return rollback(op, fmt.Sprintf("Failed to checkpoint state transition for '%s': %s", t.SkillName, e))
				}
				stateOnly++
				applied = append(applied, op)

			case op.Action == "DEACTIVATE_STATE":
				if old, ok := doc.Records[t.SkillName]; ok {
					prevRevision := doc.Revision
					old.Lifecycle, old.LastObservedSelected = "inactive", false
					doc.Records[t.SkillName] = old
					if e := persistPostImage(co, doc); e != "" {
						return rollback(op, fmt.Sprintf("Failed to journal state deactivation for '%s': %s", t.SkillName, e))
					}
					if ok, e := SaveProjectSkillState(home, doc, prevRevision); !ok {
						return rollback(op, fmt.Sprintf("Failed to commit state deactivation for '%s': %s", t.SkillName, e))
					}
					stateOnly++
				}
				if e := checkpoint(op, co, doc); e != "" {
					return rollback(op, fmt.Sprintf("Failed to checkpoint state deactivation for '%s': %s", t.SkillName, e))
				}
				applied = append(applied, op)

			case op.Action == "UPDATE" && op.DesiredRepresentation == "link":
				dd := opDirs[key(co, t.SkillName)]
				err := os.MkdirAll(filepath.Dir(dd.recovery), 0o777)
				if err == nil && lexists(t.TargetPath) {
					err = os.Rename(t.TargetPath, dd.recovery)
				}
				if err == nil {
					err = os.MkdirAll(filepath.Dir(t.TargetPath), 0o777)
				}
				if err != nil {
					return rollback(op, fmt.Sprintf("Failed to stage copy for mode switch '%s': %s", t.SkillName, pyOSError(err)))
				}
				if !safeSymlink(out, canonicalSource, t.TargetPath) {
					return rollback(op, fmt.Sprintf("Failed to create symlink during copy→link switch for '%s'", t.SkillName))
				}
				if old, ok := doc.Records[t.SkillName]; ok {
					prevRevision := doc.Revision
					doc.Records[t.SkillName] = SkillStateRecord{SkillName: t.SkillName, Representation: "copy", Lifecycle: "inactive",
						BaselineFingerprint: old.BaselineFingerprint, BaselineOrigin: old.BaselineOrigin, LastObservedSelected: false}
					if e := persistPostImage(co, doc); e != "" {
						return rollback(op, fmt.Sprintf("Failed to journal copy→link state for '%s': %s", t.SkillName, e))
					}
					if ok, e := SaveProjectSkillState(home, doc, prevRevision); !ok {
						return rollback(op, fmt.Sprintf("Failed to commit state after copy→link switch for '%s': %s", t.SkillName, e))
					}
				}
				if e := checkpoint(op, co, doc); e != "" {
					return rollback(op, fmt.Sprintf("Failed to checkpoint copy→link switch for '%s': %s", t.SkillName, e))
				}
				contentChanges++
				applied = append(applied, op)
			}
		}
	}

	journal.Phase = "committed"
	if e := writeJournal(home, journal); e != "" {
		last := SkillOperation{Action: "NOOP", RuleID: "INV-TR-00", Reason: "commit failed",
			Target: SkillTarget{WorkspaceRoot: plan.WorkspaceRoot, WorkspaceID: "ws", ProjectName: plan.ProjectName, PhysicalCheckout: plan.WorkspaceRoot}}
		if len(plan.Operations) > 0 {
			last = plan.Operations[len(plan.Operations)-1]
		}
		return rollback(last, "Failed to mark transaction committed: "+e)
	}
	var cleanupErrors []string
	for _, r := range txRootOrder {
		if exists(r) {
			if err := os.RemoveAll(r); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Sprintf("%s: %s", r, pyOSError(err)))
			}
		}
	}
	if len(cleanupErrors) > 0 {
		return SkillExecutionResult{AppliedOps: applied, SkippedOps: skipped, RecoveryRequired: true,
			ContentChanges: contentChanges, StateOnlyChanges: stateOnly,
			ErrorMessage: fmt.Sprintf("Committed transaction %s directory cleanup failed: %s. Journal retained for recovery.", txID, joinSemi(cleanupErrors))}
	}
	deleteJournal(home, txID)
	return SkillExecutionResult{AppliedOps: applied, SkippedOps: skipped, ContentChanges: contentChanges, StateOnlyChanges: stateOnly}
}

func optAny(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func pyReprOpt(s string) string {
	if s == "" {
		return "None"
	}
	return pyRepr(s)
}

func joinSemi(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += "; "
		}
		out += s
	}
	return out
}
