// A fuller port of adopt.py's build_adopt_plan/execute_adoption, covering
// global instructions (now template-fingerprint-aware, fixing the
// previous version's #1 documented gap), MCP server adoption
// (scan_mcp_servers), and subagent adoption (scan_subagents). Python's
// adopt.py has NO separate "project instructions" adoption category —
// that was a misreading of this port's own earlier scope note; project
// instructions are a project_sync.py SYNC concept (always-linked), not an
// adopt concept, and are correctly left alone here.
//
// Deliberately simplified relative to Python, documented rather than
// hidden:
//   - Template-fingerprint tracking computes fingerprints of THIS Go
//     build's own currently-embedded templates at adopt-time (not a static
//     cross-version historical table like Python's templates.py
//     TEMPLATE_HISTORY) — correct because there is no prior Go-build
//     version to stay compatible with; a future template change
//     automatically gets picked up without needing a manually-maintained
//     history list.
//   - MCP adoption: cross-agent URL-conflict merging matches Python
//     (same canonical name + different URL across agents -> conflict,
//     reported and skipped); name_style (underscore) reversal when
//     resolving a native name against an existing canonical name is
//     supported. NOT ported: agent registration adoption
//     (AgentRegistrationAdoption), the separate human-facing backup
//     directory (create_adopt_backup — this command still writes via
//     plain file I/O, crash-unsafe relative to the transaction engine used
//     elsewhere in this port; a known gap, not new to this pass), and
//     scanning secondary "external" MCP sources like a Claude Desktop
//     config outside the agent's own native path.
//   - Subagent adoption: only the "per_file" layout format whose adapter
//     declares non-nil ImportFields is scanned (currently just
//     claude_markdown — matching Python's own
//     "adapter.import_fields is not None" adoptability gate, this isn't a
//     narrower rule, just a narrower set of adapters that currently
//     declare import fields in this Go port). DSH's shared_patch layout
//     is not scanned for adoption.
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-go/internal/mcp"
	"github.com/mr-miles/aikito-go/internal/registry"
	"github.com/mr-miles/aikito-go/internal/subagent"
	"github.com/mr-miles/aikito-go/internal/sync"
	"github.com/mr-miles/aikito-go/internal/workspace"
)

// --- Template-fingerprint tracking (templates.py's TEMPLATE_HISTORY,
// scoped to only this Go build's own current templates) ---

// templateFingerprints returns the set of fingerprints this Go build
// itself currently treats as "pristine, exactly what init workspace would
// write" for a given canonical resource id. Unlike Python's historical,
// manually-curated TEMPLATE_HISTORY table, this is computed fresh from the
// embedded template bytes every time, so it can never drift out of sync
// with what this build's own `init workspace` actually writes.
func templateFingerprints(resourceID string) map[string]struct{} {
	out := map[string]struct{}{}
	add := func(content string) {
		out[workspace.FileDigestBytes([]byte(content))] = struct{}{}
	}
	switch {
	case resourceID == "global-instructions:AGENTS.md":
		if content, err := loadTemplate("global/AGENTS.md"); err == nil {
			add(content)
		}
	case strings.HasPrefix(resourceID, "agent:"):
		name := strings.TrimPrefix(resourceID, "agent:")
		if content, err := registry.BundledAgentTemplateText(name); err == nil {
			add(content)
		}
	}
	return out
}

// --- cmdAdopt ---

func cmdAdopt(args []string, stdout, stderr io.Writer, env Environment) int {
	dryRun := false
	verbose := false
	var skip []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dry-run":
			dryRun = true
		case a == "--verbose":
			verbose = true
		case a == "--skip":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "[ERROR] --skip requires a value")
				return 2
			}
			skip = append(skip, args[i])
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(stderr, "[ERROR] Unknown flag: %s\n", a)
			return 2
		default:
			// Python's adopt accepts an optional [target] positional (scope
			// hint); this Go build adopts everything it supports regardless,
			// so a positional target is accepted but has no filtering effect.
		}
	}
	skipSet := map[string]bool{}
	for _, s := range skip {
		skipSet[s] = true
	}

	aikitoDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if err := workspace.RequireCurrentLayout(aikitoDir); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	defs, err := registry.LoadAgentDefinitions(aikitoDir, env.Home)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	plan := buildAdoptPlan(aikitoDir, env.Home, defs, skipSet)

	anyChange := false
	anyConflict := false

	// --- Global instructions ---
	if plan.Instructions != nil {
		anyChange = anyChange || plan.Instructions.Action == "CREATE"
		anyConflict = anyConflict || plan.Instructions.Action == "CONFLICT"
		printInstructionsPlan(stdout, plan.Instructions, verbose, env.Home)
	}

	// --- MCP servers ---
	for _, m := range plan.MCPServers {
		anyChange = true
		fmt.Fprintf(stdout, "\n[PLAN] CREATE mcps/%s.toml\n", m.Name)
		fmt.Fprintf(stdout, "       from %s (%s)\n", displayPathRelativeToHome(m.SourceFile, env.Home), m.SourceAgent)
	}
	for _, c := range plan.MCPConflicts {
		anyConflict = true
		fmt.Fprintf(stdout, "\n[CONFLICT] MCP server '%s': %s\n", c.Name, c.Reason)
	}

	// --- Subagents ---
	for _, s := range plan.Subagents {
		anyChange = true
		fmt.Fprintf(stdout, "\n[PLAN] CREATE subagents/%s.md\n", s.Name)
		fmt.Fprintf(stdout, "       from %s (%s)\n", displayPathRelativeToHome(s.SourceFile, env.Home), s.SourceAgent)
	}

	if !anyChange && !anyConflict {
		fmt.Fprintln(stdout, "\n[INFO] Nothing to adopt: workspace already reflects every discoverable agent-native resource.")
		return 0
	}
	if anyConflict && !dryRun {
		fmt.Fprintln(stdout, "\n[BLOCKED] Conflicts found; resolve them manually or rerun with --skip, then try again.")
		return 1
	}
	if dryRun {
		fmt.Fprintln(stdout, "\n[DRY RUN] No changes written.")
		if anyConflict {
			return 1
		}
		return 0
	}

	applied := 0
	if plan.Instructions != nil && plan.Instructions.Action == "CREATE" {
		if err := applyInstructionsAdoption(plan.Instructions); err != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "[CREATE FILE] %s\n", displayPathRelativeToHome(plan.Instructions.TargetPath, env.Home))
		applied++
	}
	for _, m := range plan.MCPServers {
		if err := applyMCPAdoption(aikitoDir, m); err != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "[CREATE FILE] %s\n", displayPathRelativeToHome(filepath.Join(aikitoDir, "mcps", m.Name+".toml"), env.Home))
		applied++
	}
	for _, s := range plan.Subagents {
		if err := applySubagentAdoption(aikitoDir, s); err != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "[CREATE FILE] %s\n", displayPathRelativeToHome(filepath.Join(aikitoDir, "subagents", s.Name+".md"), env.Home))
		applied++
	}

	fmt.Fprintf(stdout, "\n[SUCCESS] Adopted %d resource(s).\n", applied)
	return 0
}

// --- Plan model ---

type instructionsAdoptionPlan struct {
	Action     string // "OK" | "CREATE" | "CONFLICT"
	TargetPath string
	SourcePath string
	SourceText string
	Candidates []instructionCandidate
}

type instructionCandidate struct {
	Agent string
	Path  string
	FP    string
}

type mcpAdoptionPlan struct {
	Name        string
	Agents      []string
	Config      map[string]any
	SourceAgent string
	SourceFile  string
}

type mcpAdoptionConflict struct {
	Name   string
	Reason string
}

type subagentAdoptionPlan struct {
	Name        string
	Description string
	Body        string
	SourceAgent string
	SourceFile  string
	TargetAgent string // the single agent this adoption targets (keep simple: one agent per adoption)
}

type adoptPlan struct {
	Instructions *instructionsAdoptionPlan
	MCPServers   []mcpAdoptionPlan
	MCPConflicts []mcpAdoptionConflict
	Subagents    []subagentAdoptionPlan
}

// buildAdoptPlan is also reused (dry-run only) by doctor.go's Adoption
// check, so it must never write anything itself.
func buildAdoptPlan(aikitoDir, home string, defs map[string]registry.AgentDefinition, skip map[string]bool) adoptPlan {
	var plan adoptPlan
	plan.Instructions = buildInstructionsPlan(aikitoDir, home, defs, skip)
	plan.MCPServers, plan.MCPConflicts = buildMCPAdoptionPlan(aikitoDir, defs, skip)
	plan.Subagents = buildSubagentAdoptionPlan(aikitoDir, defs, skip)
	return plan
}

// --- Global instructions ---

func buildInstructionsPlan(aikitoDir, home string, defs map[string]registry.AgentDefinition, skip map[string]bool) *instructionsAdoptionPlan {
	canonicalPath := filepath.Join(aikitoDir, "global", "AGENTS.md")
	canonicalFP, _ := fingerprintIfExists(canonicalPath)

	var candidates []instructionCandidate
	seenPaths := map[string]bool{}
	for _, name := range registry.BuiltinAgents {
		if skip[name] {
			continue
		}
		def, ok := defs[name]
		if !ok || def.InstructionPath == nil {
			continue
		}
		if seenPaths[*def.InstructionPath] {
			continue
		}
		fp, ferr := fingerprintIfExists(*def.InstructionPath)
		if ferr != nil || fp == "" {
			continue
		}
		seenPaths[*def.InstructionPath] = true
		candidates = append(candidates, instructionCandidate{Agent: name, Path: *def.InstructionPath, FP: fp})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Agent < candidates[j].Agent })

	plan := &instructionsAdoptionPlan{TargetPath: canonicalPath, Candidates: candidates}

	// No canonical file at all: compare() isn't needed (and its
	// empty-base-means-missing convention is for the CALLER to special-case
	// before calling it, per import_decisions.py's documented pattern — a
	// target that doesn't exist yet is always a plain CREATE, never run
	// through the ancestor comparison). Adopt the first candidate found.
	if canonicalFP == "" {
		if len(candidates) == 0 {
			plan.Action = "OK"
			return plan
		}
		plan.Action = "CREATE"
		plan.SourcePath = candidates[0].Path
		if data, rerr := os.ReadFile(candidates[0].Path); rerr == nil {
			plan.SourceText = string(data)
		}
		return plan
	}

	// Canonical exists: use the template fingerprint set as the three-way
	// compare's "ancestor" to tell "still exactly what init wrote, safe to
	// adopt over" apart from "the user customized this; leave it alone" and
	// genuine conflicts.
	base := templateFingerprints("global-instructions:AGENTS.md")
	canonicalFPPtr := &canonicalFP
	plan.Action = "OK"
	for _, c := range candidates {
		fp := c.FP
		outcome := sync.Compare(base, canonicalFPPtr, &fp)
		switch {
		case outcome.Action == "NOOP":
			// Candidate already matches canonical; nothing to do.
		case outcome.Target == "local":
			// Canonical is unchanged from the template (local==ancestor) and
			// the candidate differs: safe to adopt the candidate's content.
			if plan.Action != "CREATE" {
				plan.Action = "CREATE"
				plan.SourcePath = c.Path
				if data, rerr := os.ReadFile(c.Path); rerr == nil {
					plan.SourceText = string(data)
				}
			}
		case outcome.Target == "remote":
			// Candidate is unchanged from the template but canonical was
			// already customized by the user: nothing useful to adopt from
			// this particular candidate, and not a conflict either.
		default:
			// Both differ from the template AND from each other: genuine
			// conflict, needs human review.
			if plan.Action != "CREATE" {
				plan.Action = "CONFLICT"
			}
		}
	}
	if plan.Action == "" {
		plan.Action = "OK"
	}
	return plan
}

func printInstructionsPlan(stdout io.Writer, plan *instructionsAdoptionPlan, verbose bool, home string) {
	switch plan.Action {
	case "OK":
		fmt.Fprintf(stdout, "[OK] Canonical global instructions already present: %s\n", displayPathRelativeToHome(plan.TargetPath, home))
	case "CREATE":
		fmt.Fprintf(stdout, "\n[PLAN] CREATE %s\n", displayPathRelativeToHome(plan.TargetPath, home))
		fmt.Fprintf(stdout, "       from %s\n", displayPathRelativeToHome(plan.SourcePath, home))
	case "CONFLICT":
		fmt.Fprintf(stdout, "\n[CONFLICT] Global instructions: canonical content diverges from agent-native source(s); review manually.\n")
	}
	if verbose {
		for _, c := range plan.Candidates {
			fmt.Fprintf(stdout, "  %-14s %s\n", c.Agent, displayPathRelativeToHome(c.Path, home))
		}
	}
}

func applyInstructionsAdoption(plan *instructionsAdoptionPlan) error {
	if err := os.MkdirAll(filepath.Dir(plan.TargetPath), 0o777); err != nil {
		return err
	}
	return os.WriteFile(plan.TargetPath, []byte(plan.SourceText), 0o644)
}

func fingerprintIfExists(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", nil
	}
	if !info.Mode().IsRegular() {
		return "", nil
	}
	return workspace.FileDigest(path)
}

// --- MCP server adoption ---

func buildMCPAdoptionPlan(aikitoDir string, defs map[string]registry.AgentDefinition, skip map[string]bool) ([]mcpAdoptionPlan, []mcpAdoptionConflict) {
	existing := map[string]bool{}
	mcpsDir := filepath.Join(aikitoDir, "mcps")
	if entries, err := os.ReadDir(mcpsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".toml") {
				existing[strings.TrimSuffix(e.Name(), ".toml")] = true
			}
		}
	}

	type adopted struct {
		name, sourceAgent, sourceFile string
		agents                        []string
		config                        map[string]any
	}
	byName := map[string]*adopted{}
	var order []string
	var conflicts []mcpAdoptionConflict

	targetName := func(def registry.AgentDefinition, canonical string) string {
		if def.MCP != nil && def.MCP.NameStyle == "underscore" {
			return strings.ReplaceAll(canonical, "-", "_")
		}
		return canonical
	}
	resolveCanonical := func(def registry.AgentDefinition, rawName string) string {
		if existing[rawName] {
			return rawName
		}
		for canon := range existing {
			if targetName(def, canon) == rawName {
				return canon
			}
		}
		if _, ok := byName[rawName]; ok {
			return rawName
		}
		for canon, a := range byName {
			if targetName(def, canon) == rawName {
				_ = a
				return canon
			}
		}
		return rawName
	}

	for _, name := range registry.BuiltinAgents {
		if skip[name] {
			continue
		}
		def, ok := defs[name]
		if !ok || def.MCP == nil || !def.MCP.IsSupported() {
			continue
		}
		adapter, aerr := mcp.GetMCPAdapter(def.MCP.Adapter)
		if aerr != nil || adapter.ImportEntry == nil {
			continue
		}
		data, rerr := os.ReadFile(def.MCP.ConfigPath)
		if rerr != nil {
			continue
		}
		entries, eerr := adapter.ReadAllEntries(string(data))
		if eerr != nil || entries == nil {
			continue
		}
		builtin := map[string]bool{}
		for _, b := range def.MCP.BuiltinServers {
			builtin[b] = true
		}
		for _, rawName := range entries.Keys() {
			v, _ := entries.Get(rawName)
			entry, _ := v.(*mcp.OrderedObject)
			if entry == nil {
				continue
			}
			converted := adapter.ImportEntry(entry)
			if converted == nil {
				continue
			}
			canon := resolveCanonical(def, rawName)
			if builtin[targetName(def, canon)] {
				continue
			}
			if existing[canon] {
				// Already canonically represented: adoption discovers NEW
				// resources, it doesn't re-propose or reconcile drift for
				// ones that already have a mcps/<name>.toml (that's sync's
				// job, via its own conflict/update machinery, not adopt's).
				continue
			}
			cfg := canonicalMCPConfig(converted)
			if a, ok := byName[canon]; ok {
				if fmt.Sprint(a.config["url"]) != fmt.Sprint(cfg["url"]) || fmt.Sprint(a.config["command"]) != fmt.Sprint(cfg["command"]) {
					conflicts = append(conflicts, mcpAdoptionConflict{
						Name:   canon,
						Reason: fmt.Sprintf("'%s' (from %s) and '%s' (from %s) disagree on url/command", a.sourceAgent, a.sourceFile, name, def.MCP.ConfigPath),
					})
					continue
				}
				found := false
				for _, ag := range a.agents {
					if ag == name {
						found = true
						break
					}
				}
				if !found {
					a.agents = append(a.agents, name)
				}
				continue
			}
			byName[canon] = &adopted{name: canon, sourceAgent: name, sourceFile: def.MCP.ConfigPath, agents: []string{name}, config: cfg}
			order = append(order, canon)
		}
	}

	var out []mcpAdoptionPlan
	for _, name := range order {
		a := byName[name]
		out = append(out, mcpAdoptionPlan{
			Name: a.name, Agents: a.agents, Config: a.config,
			SourceAgent: a.sourceAgent, SourceFile: a.sourceFile,
		})
	}
	return out, conflicts
}

func canonicalMCPConfig(entry *mcp.OrderedObject) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"command", "url", "args", "env", "transport", "headers"} {
		if v, ok := entry.Get(k); ok && v != nil {
			out[k] = v
		}
	}
	if _, hasURL := out["url"]; hasURL {
		if _, hasTransport := out["transport"]; !hasTransport {
			out["transport"] = "remote"
		}
	}
	return out
}

func applyMCPAdoption(aikitoDir string, m mcpAdoptionPlan) error {
	var lines []string
	isRemote := m.Config["url"] != nil
	if isRemote {
		rawURL := fmt.Sprint(m.Config["url"])
		safeURL, warnings := SanitizeMCPURL(rawURL, m.Name)
		for range warnings {
			// Adoption runs non-interactively here; sanitization warnings
			// are still applied (the secret is still stripped), just not
			// re-printed per-field in this helper — the CLI prints its own
			// summary line per adopted server.
		}
		lines = append(lines, `transport = "remote"`, fmt.Sprintf("url = %s", workspace.CanonicalJSON(safeURL)))
	} else {
		lines = append(lines, fmt.Sprintf("command = %s", workspace.CanonicalJSON(fmt.Sprint(m.Config["command"]))))
		if args, ok := m.Config["args"]; ok {
			lines = append(lines, fmt.Sprintf("args = %s", sync.FormatTomlValue(args)))
		}
	}
	lines = append(lines, fmt.Sprintf("agents = %s", workspace.CanonicalJSON(toAnySlice(m.Agents))))
	if headers, ok := m.Config["headers"].(map[string]any); ok && len(headers) > 0 {
		headersStr := make(map[string]string, len(headers))
		for k, v := range headers {
			headersStr[k] = fmt.Sprint(v)
		}
		sanitized, _ := SanitizeMCPHeaders(headersStr, m.Name)
		sanitizedAny := make(map[string]any, len(sanitized))
		for k, v := range sanitized {
			sanitizedAny[k] = v
		}
		lines = append(lines, "headers = "+sync.FormatTomlValue(sanitizedAny))
	}
	if env, ok := m.Config["env"]; ok {
		lines = append(lines, fmt.Sprintf("env = %s", sync.FormatTomlValue(env)))
	}

	mcpsDir := filepath.Join(aikitoDir, "mcps")
	if err := os.MkdirAll(mcpsDir, 0o777); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(mcpsDir, m.Name+".toml"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// --- Subagent adoption ---

func buildSubagentAdoptionPlan(aikitoDir string, defs map[string]registry.AgentDefinition, skip map[string]bool) []subagentAdoptionPlan {
	canonicalDir := filepath.Join(aikitoDir, "subagents")
	existing := map[string]bool{}
	if entries, err := os.ReadDir(canonicalDir); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".md") {
				existing[strings.TrimSuffix(e.Name(), ".md")] = true
			}
		}
	}

	seen := map[string]bool{}
	var out []subagentAdoptionPlan
	for _, name := range registry.BuiltinAgents {
		if skip[name] {
			continue
		}
		def, ok := defs[name]
		if !ok || def.Subagents == nil {
			continue
		}
		adapter, aerr := subagent.GetSubagentAdapter(def.Subagents.ConfigFormat)
		if aerr != nil || adapter.Layout != subagent.LayoutPerFile || adapter.ImportFields == nil {
			continue
		}
		entries, derr := os.ReadDir(def.Subagents.ConfigPath)
		if derr != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			stem := strings.TrimSuffix(e.Name(), ".md")
			if existing[stem] || seen[stem] {
				continue
			}
			full := filepath.Join(def.Subagents.ConfigPath, e.Name())
			if subagent.HasAikitoMarker(full) {
				continue // already aikito-managed, nothing to adopt
			}
			data, rerr := os.ReadFile(full)
			if rerr != nil {
				continue
			}
			meta, body := parseSimpleMarkdownFrontmatter(string(data))
			if strings.TrimSpace(body) == "" {
				continue
			}
			desc, _ := meta["description"].(string)
			if desc == "" {
				desc = fmt.Sprintf("Adopted subagent %s from %s", stem, def.DisplayName)
			}
			seen[stem] = true
			out = append(out, subagentAdoptionPlan{
				Name: stem, Description: desc, Body: body,
				SourceAgent: name, SourceFile: full, TargetAgent: name,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func applySubagentAdoption(aikitoDir string, s subagentAdoptionPlan) error {
	metadata := map[string]any{
		"description": s.Description,
		"agents":      []any{s.TargetAgent},
	}
	content := workspace.RenderSubagentText(metadata, strings.TrimRight(s.Body, "\n")+"\n", "")
	dir := filepath.Join(aikitoDir, "subagents")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, s.Name+".md"), []byte(content), 0o644)
}

func strp(s string) *string { return &s }
