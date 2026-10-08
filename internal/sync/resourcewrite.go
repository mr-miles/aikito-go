// Resource-write pipeline: translate a batch of logical resource writes
// (ResourceWrite) into one verified filesystem transaction via the Apply
// primitive in transactions.go. Ported from
// aikito/src/aikito/workspace/resource_write.py.
//
// Package-placement note: Python's resource_write.py lives in aikito's
// workspace/ package next to transactions.py. In this Go port the
// transaction engine ended up in this separate internal/sync package
// (because it needs internal/workspace's fingerprint functions, and Go
// forbids import cycles) — so the resource-write pipeline lives here too,
// since it needs BOTH workspace.Resource-shaped data AND Apply/Change.
package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/mr-miles/aikito-rs/internal/workspace"
)

// ResourceWrite is one logical write or deletion, keyed by (Kind, Name).
// Fingerprint nil means "delete this resource"; SourcePath is where to copy
// new content from (ignored for deletions).
type ResourceWrite struct {
	RelativePath string
	Kind         string
	Fingerprint  *string
	Name         string
	SourcePath   string
	Before       *string
}

func (w ResourceWrite) ID() string { return w.Kind + ":" + w.Name }

// ResourceContent is validated resource metadata and content locations,
// independent of layout — the write pipeline's view of "what a batch of
// writes may read from" (either the canonical workspace being synced from,
// or an import source).
type ResourceContent struct {
	Resources     map[string]workspace.Resource
	Paths         map[string]string // id -> absolute path
	WorkspaceRoot string            // "" if none (sync callers always set it; import's legacy path may not)
	Values        map[string]TomlValue
}

// ResourceContentFromWorkspace mirrors ResourceContent.from_workspace: binds
// every config/project-field resource's current typed TOML value (re-
// validating it still matches the resource's recorded fingerprint — a
// stale source aborts immediately) alongside a path index for every
// resource in the snapshot.
func ResourceContentFromWorkspace(snapshot *workspace.WorkspaceSnapshot) (ResourceContent, error) {
	values := map[string]TomlValue{}
	documents := map[string]map[string]any{}
	paths := map[string]string{}

	for key, resource := range snapshot.Resources {
		paths[key] = filepath.Join(snapshot.Root, filepath.FromSlash(resource.Parts[0].Path))
		if resource.Kind != "config" && resource.Kind != "project-field" {
			continue
		}
		path := paths[key]
		document, ok := documents[path]
		if !ok {
			data, err := os.ReadFile(path)
			if err != nil {
				return ResourceContent{}, fmt.Errorf("cannot read %s: %w", path, err)
			}
			document, err = workspace.DecodeTOML(data)
			if err != nil {
				return ResourceContent{}, fmt.Errorf("invalid TOML content: %s: %w", path, err)
			}
			documents[path] = document
		}

		var value *TomlValue
		if resource.Kind == "config" {
			if v, ok := DocumentValues(document, nil)[resource.Name]; ok {
				vv := v
				value = &vv
			}
		} else {
			member := partitionAfterSlash(resource.Name)
			if v, ok := document[member]; ok {
				vv := TomlValue{Path: []string{member}, Value: v}
				value = &vv
			}
		}
		if value == nil || workspace.ValueFingerprint(value.Value) != resource.Fingerprint {
			return ResourceContent{}, &WorkspaceCoreError{Message: fmt.Sprintf("Source field changed: %s", key)}
		}
		values[key] = *value
	}

	return ResourceContent{
		Resources:     snapshot.Resources,
		Paths:         paths,
		WorkspaceRoot: snapshot.Root,
		Values:        values,
	}, nil
}

// PartitionWrites groups whole-file replacements (returned sorted, for
// deterministic processing order) separately from changes targeting shared
// files (which must go through the merge renderers in tomlrender.go).
func PartitionWrites(writes []ResourceWrite) (copies []string, merges []ResourceWrite) {
	copySet := map[string]bool{}
	for _, w := range writes {
		if workspace.IsSharedResource(w.Kind) {
			merges = append(merges, w)
		} else {
			copySet[w.RelativePath] = true
		}
	}
	for p := range copySet {
		copies = append(copies, p)
	}
	sort.Strings(copies)
	return copies, merges
}

// missingIDs returns resource.References not present in resources/external,
// excluding a reference to a bundled skill (those are never "missing" even
// though no skill: resource exists for them — they're system-managed
// snapshots, not ordinary resources).
func missingIDs(resource workspace.Resource, resources map[string]workspace.Resource, external map[string]bool) []string {
	var out []string
	for _, ref := range resource.References {
		if _, ok := resources[ref]; ok {
			continue
		}
		if external[ref] {
			continue
		}
		if len(ref) > len("skill:") && ref[:len("skill:")] == "skill:" && workspace.IsBundledSkillName(ref[len("skill:"):]) {
			continue
		}
		out = append(out, ref)
	}
	return out
}

// MissingReferences reports every "<id> references missing <ref>" problem
// across resources, sorted and deduplicated.
func MissingReferences(resources map[string]workspace.Resource, external map[string]bool) []string {
	set := map[string]bool{}
	for _, resource := range resources {
		for _, ref := range missingIDs(resource, resources, external) {
			set[resource.ID()+" references missing "+ref] = true
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// requireValid mirrors _require_valid: a non-empty snapshot.Findings (and,
// when references is true, any MissingReferences problem) becomes a hard
// error — this is all-or-nothing validation, not a Findings-collection
// pattern like the scanner itself uses.
func requireValid(snapshot *workspace.WorkspaceSnapshot, checkReferences bool) error {
	var problems []string
	for _, f := range snapshot.Findings {
		problems = append(problems, f.Resource+": "+f.Message)
	}
	if checkReferences {
		problems = append(problems, MissingReferences(snapshot.Resources, nil)...)
	}
	if len(problems) > 0 {
		msg := "Invalid workspace resources: "
		for i, p := range problems {
			if i > 0 {
				msg += "; "
			}
			msg += p
		}
		return &WorkspaceCoreError{Message: msg}
	}
	return nil
}

// VerifyResourceSnapshot mirrors verify_resource_snapshot: the Apply()
// verify callback run after resource renames land, before state writes —
// re-snapshots the target workspace from scratch and asserts the resulting
// resource map (fingerprints AND mode_fingerprints) exactly matches what was
// expected going in.
func VerifyResourceSnapshot(actual *workspace.WorkspaceSnapshot, expected map[string]workspace.Resource, external map[string]bool) error {
	if err := requireValid(actual, false); err != nil {
		return err
	}
	if findings := MissingReferences(actual.Resources, external); len(findings) > 0 {
		msg := "Invalid resulting references: "
		for i, f := range findings {
			if i > 0 {
				msg += ", "
			}
			msg += f
		}
		return &WorkspaceCoreError{Message: msg}
	}

	type pair struct {
		fingerprint string
		mode        string // "" sentinel for nil, since mode fingerprints are always 64-hex-char when present
		hasMode     bool
	}
	actualPairs := map[string]pair{}
	for key, r := range actual.Resources {
		p := pair{fingerprint: r.Fingerprint}
		if exp, ok := expected[key]; ok && exp.ModeFingerprint != nil {
			if r.ModeFingerprint != nil {
				p.mode, p.hasMode = *r.ModeFingerprint, true
			}
		}
		actualPairs[key] = p
	}
	intendedPairs := map[string]pair{}
	for key, r := range expected {
		p := pair{fingerprint: r.Fingerprint}
		if r.ModeFingerprint != nil {
			p.mode, p.hasMode = *r.ModeFingerprint, true
		}
		intendedPairs[key] = p
	}

	allKeys := map[string]bool{}
	for k := range actualPairs {
		allKeys[k] = true
	}
	for k := range intendedPairs {
		allKeys[k] = true
	}
	var changed []string
	for k := range allKeys {
		if actualPairs[k] != intendedPairs[k] {
			changed = append(changed, k)
		}
	}
	if len(changed) > 0 {
		sort.Strings(changed)
		msg := "Resource verification failed: "
		for i, c := range changed {
			if i > 0 {
				msg += ", "
			}
			msg += c
		}
		return &WorkspaceCoreError{Message: msg}
	}
	return nil
}

// ValidateSubagentBatchFunc is the extension point standing in for
// _validate_subagent_batch: validate every written subagent, plus every
// untouched subagent whose referenced agent definitions changed in this
// same batch, against the resulting agent registry. This cross-resource-
// kind validation coupling needs subagent_validation.py's Go equivalent,
// which doesn't exist yet in this port (being built separately in
// internal/subagent) — wiring it in is a follow-up; until then this is a
// no-op so the rest of the pipeline is independently usable and testable.
//
// expected is the resulting resource map, paths is id -> absolute content
// path, home anchors subagent platform-path resolution, and written is the
// set of resource IDs actually touched by this batch (vs. untouched
// resources whose platform validity can still be affected by a changed
// agent definition in the same batch).
type ValidateSubagentBatchFunc func(expected map[string]workspace.Resource, paths map[string]string, home string, written map[string]bool) error

// NoopValidateSubagentBatch is the default ValidateSubagentBatchFunc until
// internal/subagent exists and a real validator can be wired in.
func NoopValidateSubagentBatch(map[string]workspace.Resource, map[string]string, string, map[string]bool) error {
	return nil
}

// PrepareResourceWrites mirrors prepare_resource_writes: compose logical
// writes/deletions into transactions.Change values plus the resulting
// expected resource map, without touching the real filesystem outside the
// supplied staging directory. validateSubagentBatch may be nil (defaults to
// a no-op, see ValidateSubagentBatchFunc's doc comment).
func PrepareResourceWrites(
	content ResourceContent,
	targetSnapshot *workspace.WorkspaceSnapshot,
	writes []ResourceWrite,
	staging string,
	policy PathPolicy,
	classify Classifier,
	external map[string]bool,
	sync bool,
	home string,
	validateSubagentBatch ValidateSubagentBatchFunc,
) ([]Change, map[string]workspace.Resource, error) {
	if validateSubagentBatch == nil {
		validateSubagentBatch = NoopValidateSubagentBatch
	}
	target := targetSnapshot.Root
	if err := requireValid(targetSnapshot, false); err != nil {
		return nil, nil, err
	}

	expected := map[string]workspace.Resource{}
	for k, v := range targetSnapshot.Resources {
		expected[k] = v
	}

	type pathGroup struct {
		path  string
		items []ResourceWrite
	}
	byPath := map[string]*pathGroup{}
	var pathOrder []string
	addToGroup := func(w ResourceWrite) {
		g, ok := byPath[w.RelativePath]
		if !ok {
			g = &pathGroup{path: w.RelativePath}
			byPath[w.RelativePath] = g
			pathOrder = append(pathOrder, w.RelativePath)
		}
		g.items = append(g.items, w)
	}

	for _, write := range writes {
		remote, hasRemote := content.Resources[write.ID()]
		local, hasLocal := targetSnapshot.Resources[write.ID()]

		var localFp *string
		if hasLocal {
			localFp = &local.Fingerprint
		}
		if !strPtrEq(localFp, write.Before) {
			return nil, nil, &WorkspaceCoreError{Message: fmt.Sprintf("Target changed before writing: %s", write.ID())}
		}

		if write.Fingerprint == nil {
			if !hasLocal || (workspace.IsSharedResource(write.Kind) && !sync) {
				return nil, nil, &WorkspaceCoreError{Message: fmt.Sprintf("Unsupported resource deletion: %s", write.ID())}
			}
			if write.RelativePath != local.Parts[0].Path {
				return nil, nil, &WorkspaceCoreError{Message: fmt.Sprintf("Unexpected destination path: %s", write.ID())}
			}
			delete(expected, write.ID())
		} else {
			if !hasRemote || remote.Fingerprint != *write.Fingerprint {
				return nil, nil, &WorkspaceCoreError{Message: fmt.Sprintf("Source changed before writing: %s", write.ID())}
			}
			sourcePath := write.SourcePath
			if sourcePath == "" {
				sourcePath = remote.Parts[0].Path
			}
			if sourcePath != remote.Parts[0].Path {
				return nil, nil, &WorkspaceCoreError{Message: fmt.Sprintf("Unexpected source path: %s", write.ID())}
			}
			if write.Kind != "inbox" && write.RelativePath != sourcePath {
				return nil, nil, &WorkspaceCoreError{Message: fmt.Sprintf("Unexpected destination path: %s", write.ID())}
			}
			newParts := append([]workspace.ResourcePart{{Path: write.RelativePath, Table: remote.Parts[0].Table}}, remote.Parts[1:]...)
			updated := remote
			updated.Parts = newParts
			expected[write.ID()] = updated
		}
		addToGroup(write)
	}

	if findings := MissingReferences(expected, external); len(findings) > 0 {
		msg := "Invalid resulting references: "
		for i, f := range findings {
			if i > 0 {
				msg += "; "
			}
			msg += f
		}
		return nil, nil, &WorkspaceCoreError{Message: msg}
	}

	type storageEntry struct {
		kind    string
		current *Version
	}
	storage := map[string]storageEntry{}
	for _, relative := range pathOrder {
		g := byPath[relative]
		kinds := map[string]bool{}
		for _, w := range g.items {
			kinds[workspace.PhysicalKind(w.Kind)] = true
		}
		if len(kinds) != 1 {
			return nil, nil, &WorkspaceCoreError{Message: fmt.Sprintf("Conflicting storage kinds: %s", relative)}
		}
		var kind string
		for k := range kinds {
			kind = k
		}
		current, err := VersionAt(target, relative, kind, policy, classify)
		if err != nil {
			return nil, nil, err
		}
		first := g.items[0]
		if first.Kind == "memory" || first.Kind == "project-memory" || first.Kind == "skill" {
			var currentFp *string
			if current != nil {
				currentFp = &current.Fingerprint
			}
			if !strPtrEq(currentFp, first.Before) {
				return nil, nil, &WorkspaceCoreError{Message: fmt.Sprintf("Unmanaged or changed target resource: %s", relative)}
			}
		}
		storage[relative] = storageEntry{kind: kind, current: current}
	}

	_, merges := PartitionWrites(writes)
	if len(merges) > 0 && !sync && content.WorkspaceRoot == "" {
		return nil, nil, &WorkspaceCoreError{Message: "Shared TOML writes require workspace content"}
	}

	rendered := map[string]*string{}
	if sync {
		for _, relative := range pathOrder {
			g := byPath[relative]
			if !workspace.IsSharedResource(g.items[0].Kind) {
				continue
			}
			var text string
			if storage[relative].current != nil {
				data, err := os.ReadFile(joinPosix(target, relative))
				if err != nil {
					return nil, nil, err
				}
				text = string(data)
			}
			for _, w := range g.items {
				if w.Fingerprint != nil && (w.Kind == "config" || w.Kind == "project-field") {
					value, ok := content.Values[w.ID()]
					if !ok || workspace.ValueFingerprint(value.Value) != *w.Fingerprint {
						return nil, nil, &WorkspaceCoreError{Message: fmt.Sprintf("Source field changed: %s", w.ID())}
					}
				}
			}
			out, err := RenderSyncFile(text, g.items, content.Values, expected)
			if err != nil {
				return nil, nil, err
			}
			rendered[relative] = out
		}
	} else if len(merges) > 0 {
		merged, err := RenderMergedFiles(content.WorkspaceRoot, target, merges, home)
		if err != nil {
			return nil, nil, err
		}
		for k, v := range merged {
			vv := v
			rendered[k] = &vv
		}
	}

	var changes []Change
	sortedPaths := append([]string(nil), pathOrder...)
	sort.Strings(sortedPaths)
	for _, relative := range sortedPaths {
		g := byPath[relative]
		entry := storage[relative]
		var source string
		var after *string

		if text, ok := rendered[relative]; ok {
			if text != nil {
				stagePath := filepath.Join(staging, filepath.FromSlash(relative))
				if err := os.MkdirAll(filepath.Dir(stagePath), 0o777); err != nil {
					return nil, nil, err
				}
				if err := os.WriteFile(stagePath, []byte(*text), 0o644); err != nil {
					return nil, nil, err
				}
				source = stagePath
				fp, err := workspace.FingerprintResource(stagePath, entry.kind)
				if err != nil {
					return nil, nil, err
				}
				after = &fp
			}
		} else {
			allNil, anyNil := true, false
			for _, w := range g.items {
				if w.Fingerprint == nil {
					anyNil = true
				} else {
					allNil = false
				}
			}
			switch {
			case allNil:
				// deletion, no source/after
			case anyNil:
				return nil, nil, &WorkspaceCoreError{Message: fmt.Sprintf("Conflicting deletion and update: %s", relative)}
			default:
				pathSet := map[string]bool{}
				for _, w := range g.items {
					if p, ok := content.Paths[w.ID()]; ok {
						pathSet[p] = true
					} else {
						pathSet[""] = true
					}
				}
				if len(pathSet) != 1 || pathSet[""] {
					return nil, nil, &WorkspaceCoreError{Message: fmt.Sprintf("Missing or conflicting content: %s", relative)}
				}
				var srcPath string
				for p := range pathSet {
					srcPath = p
				}
				if entry.kind == "skill" {
					if info, err := os.Stat(filepath.Join(srcPath, "SKILL.md")); err != nil || !info.Mode().IsRegular() {
						return nil, nil, &WorkspaceCoreError{Message: fmt.Sprintf("Skill content lacks a regular SKILL.md: %s", g.items[0].ID())}
					}
				}
				logical, references, err := workspace.InspectResourceContent(expected[g.items[0].ID()], srcPath)
				if err != nil {
					return nil, nil, err
				}
				expectedRes := expected[g.items[0].ID()]
				if logical != *g.items[0].Fingerprint || !stringSlicesEqual(references, expectedRes.References) {
					return nil, nil, &WorkspaceCoreError{Message: fmt.Sprintf("Source changed before writing: %s", g.items[0].ID())}
				}
				fp, err := workspace.FingerprintResource(srcPath, entry.kind)
				if err != nil {
					return nil, nil, err
				}
				if (g.items[0].Kind == "memory" || g.items[0].Kind == "project-memory" || g.items[0].Kind == "skill") && fp != *g.items[0].Fingerprint {
					return nil, nil, &WorkspaceCoreError{Message: fmt.Sprintf("Source changed before writing: %s", g.items[0].ID())}
				}
				source = srcPath
				after = &fp
			}
		}

		var before *string
		if entry.current != nil {
			before = &entry.current.Fingerprint
		}
		changes = append(changes, Change{
			Target: 0,
			Path:   relative,
			Kind:   entry.kind,
			Source: source,
			Before: before,
			After:  after,
		})
	}

	effectivePaths := map[string]string{}
	for key, resource := range expected {
		effectivePaths[key] = filepath.Join(target, filepath.FromSlash(resource.Parts[0].Path))
	}
	for _, change := range changes {
		if change.Source == "" {
			continue
		}
		for key, resource := range expected {
			if resource.Parts[0].Path == change.Path {
				effectivePaths[key] = change.Source
			}
		}
	}

	effectiveHome := home
	if effectiveHome == "" {
		effectiveHome = target
	}
	written := map[string]bool{}
	for _, w := range writes {
		written[w.ID()] = true
	}
	if err := validateSubagentBatch(expected, effectivePaths, effectiveHome, written); err != nil {
		return nil, nil, err
	}

	return changes, expected, nil
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ApplyResourceWrites mirrors apply_resource_writes: prepare the batch into
// a temp staging directory, then commit it through Apply under the
// caller's writer lock, verifying the result via VerifyResourceSnapshot.
func ApplyResourceWrites(
	sourceSnapshot, targetSnapshot *workspace.WorkspaceSnapshot,
	writes []ResourceWrite,
	policy PathPolicy,
	classify Classifier,
	home string,
	validateSubagentBatch ValidateSubagentBatchFunc,
) error {
	if err := requireValid(sourceSnapshot, false); err != nil {
		return err
	}
	content, err := ResourceContentFromWorkspace(sourceSnapshot)
	if err != nil {
		return err
	}

	staging, err := os.MkdirTemp("", "aikito-resource-write-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	changes, expected, err := PrepareResourceWrites(
		content, targetSnapshot, writes, staging, policy, classify, nil, false, home, validateSubagentBatch,
	)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}

	verify := func() error {
		actual, err := workspace.SnapshotWorkspace(targetSnapshot.Root, home)
		if err != nil {
			return err
		}
		return VerifyResourceSnapshot(actual, expected, nil)
	}
	return Apply([]string{targetSnapshot.Root}, changes, nil, verify, policy, classify)
}
