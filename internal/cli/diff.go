package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-go/internal/mcp"
	"github.com/mr-miles/aikito-go/internal/project"
	"github.com/mr-miles/aikito-go/internal/registry"
	"github.com/mr-miles/aikito-go/internal/subagent"
	"github.com/mr-miles/aikito-go/internal/workspace"
)

// driftDiff mirrors diff_model.py's DriftDiff: a structured, machine-
// filterable identity plus a pre-rendered unified-diff string.
type driftDiff struct {
	kind     string // "mcp" | "subagent" | "project_skill"
	name     string
	agent    string
	diff     string
	project  string
	file     string
	checkout string
}

func (d driftDiff) displayLabel() string {
	switch d.kind {
	case "project_skill":
		label := fmt.Sprintf("Project %s/skill %s — %s", d.project, d.name, d.file)
		if d.checkout != "" {
			return label + " (" + d.checkout + ")"
		}
		return label
	case "mcp":
		return fmt.Sprintf("MCP %s/%s", d.agent, d.name)
	case "subagent":
		return fmt.Sprintf("Subagent %s/%s", d.agent, d.name)
	default:
		return d.name
	}
}

// cmdDiff ports cli.py's cmd_diff: the drift index, --all, and the
// project / mcp / subagent drill-downs.
func cmdDiff(args []string, stdout, stderr io.Writer, env Environment) int {
	// Parent options come before the subcommand, as with argparse.
	all := false
	i := 0
	var parentExtras []string
	for ; i < len(args); i++ {
		a := args[i]
		if a == "project" || a == "mcp" || a == "subagent" {
			break
		}
		if a == "--all" || (strings.HasPrefix(a, "--a") && strings.HasPrefix("--all", a)) {
			all = true
			continue
		}
		parentExtras = append(parentExtras, a)
	}
	target, rest := "", []string(nil)
	if i < len(args) {
		target, rest = args[i], args[i+1:]
	}
	if len(parentExtras) > 0 {
		if !strings.HasPrefix(parentExtras[0], "-") {
			fmt.Fprintf(stderr, "%saikito diff: error: argument diff_target: invalid choice: '%s' (choose from project, mcp, subagent)\n", subcommandUsage("diff"), parentExtras[0])
			return 2
		}
		fmt.Fprintf(stderr, "%saikito: error: unrecognized arguments: %s\n", rootUsage(), strings.Join(parentExtras, " "))
		return 2
	}
	var positionals []string
	if target != "" {
		maxPos, required := 3, []string(nil)
		switch target {
		case "mcp":
			maxPos, required = 2, []string{"agent", "server"}
		case "subagent":
			maxPos, required = 2, []string{"agent", "name"}
		}
		parsed, ok := parseArgparseOpts("diff "+target, rest, nil, nil, nil, maxPos, stderr)
		if !ok {
			return 2
		}
		positionals = parsed.positionals
		if len(positionals) < len(required) {
			fmt.Fprintf(stderr, "%saikito diff %s: error: the following arguments are required: %s\n",
				subcommandUsage("diff "+target), target, strings.Join(required[len(positionals):], ", "))
			return 2
		}
	}

	aikitoDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if err := requireLayoutLikePython(aikitoDir); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	collect := func(kind string, projectFilter *string) ([]driftDiff, bool) {
		diffs, err := collectDriftDiffs(aikitoDir, env.Home, kind, projectFilter)
		if err != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", err)
			return nil, false
		}
		return diffs, true
	}
	if all {
		diffs, ok := collect("", nil)
		if !ok {
			return 1
		}
		fmt.Fprintln(stdout, renderDriftDiffs(diffs, "No drift detected."))
		return 0
	}
	switch target {
	case "project":
		projectName := ""
		if len(positionals) > 0 {
			projectName = positionals[0]
		}
		if projectName == "" {
			detected, derr := project.DetectCurrentProject(aikitoDir, env.Cwd, env.Home)
			var conflict *project.ContextConflictError
			if errors.As(derr, &conflict) {
				fmt.Fprintf(stderr, "[CONFLICT] Multiple projects match current directory '%s': %s\n", conflict.Path, strings.Join(conflict.Projects, ", "))
				return 1
			}
			if detected == "" {
				fmt.Fprintln(stderr, "[ERROR] No project specified and current directory is not inside any registered project.\n"+
					"Usage: aikito diff project <project> [skill] [file]")
				return 1
			}
			fmt.Fprintf(stdout, "[aikito] Target project: '%s' (detected from cwd)\n", detected)
			projectName = detected
		}
		diffs, ok := collect("project_skill", &projectName)
		if !ok {
			return 1
		}
		switch {
		case len(positionals) > 2:
			skill := positionals[1]
			matching := filterDriftDiffs(diffs, driftFilter{kind: "project_skill", project: &projectName, name: &skill, file: &positionals[2]})
			fmt.Fprintln(stdout, renderDriftDiffs(matching, "No matching drift detected."))
		case len(positionals) > 1:
			matching := filterDriftDiffs(diffs, driftFilter{kind: "project_skill", project: &projectName, name: &positionals[1]})
			fmt.Fprintln(stdout, renderDriftDiffs(matching, "No matching drift detected."))
		default:
			fmt.Fprintln(stdout, renderProjectDriftIndex(projectName, diffs))
		}
		return 0
	case "mcp", "subagent":
		diffs, ok := collect(target, nil)
		if !ok {
			return 1
		}
		matching := filterDriftDiffs(diffs, driftFilter{kind: target, agent: &positionals[0], name: &positionals[1]})
		fmt.Fprintln(stdout, renderDriftDiffs(matching, "No matching drift detected."))
		return 0
	}
	diffs, ok := collect("", nil)
	if !ok {
		return 1
	}
	fmt.Fprintln(stdout, renderDriftIndex(diffs))
	return 0
}

// driftFilter holds filter_drift_diffs' keyword arguments (nil = unset).
type driftFilter struct {
	kind                       string
	project, name, file, agent *string
}

// filterDriftDiffs is diff.py's filter_drift_diffs.
func filterDriftDiffs(diffs []driftDiff, f driftFilter) []driftDiff {
	var file *string
	if f.file != nil {
		norm := path.Clean(strings.ReplaceAll(*f.file, "\\", "/"))
		file = &norm
	}
	var out []driftDiff
	for _, d := range diffs {
		switch {
		case f.kind != "" && d.kind != f.kind,
			f.project != nil && d.project != *f.project,
			f.name != nil && d.name != *f.name,
			file != nil && d.file != *file,
			f.agent != nil && d.agent != *f.agent:
			continue
		}
		out = append(out, d)
	}
	return out
}

// collectDriftDiffs is diff.py's collect_drift_diffs; kind "" collects all
// kinds, projectFilter limits project skills.
func collectDriftDiffs(aikitoDir, home, kind string, projectFilter *string) ([]driftDiff, error) {
	var results []driftDiff
	if kind == "" || kind == "mcp" {
		mcpDiffs, err := collectMCPDriftDiffs(aikitoDir, home)
		if err != nil {
			return nil, err
		}
		results = append(results, mcpDiffs...)
	}
	if kind == "" || kind == "subagent" {
		results = append(results, collectSubagentDriftDiffs(aikitoDir, home)...)
	}
	if kind == "" || kind == "project_skill" {
		results = append(results, collectProjectSkillDiffs(aikitoDir, home, projectFilter)...)
	}
	return results, nil
}

// collectProjectSkillDiffs is project.py's collect_project_skill_diffs:
// per-file diffs for every drifted copied project skill.
func collectProjectSkillDiffs(aikitoDir, home string, projectFilter *string) []driftDiff {
	var results []driftDiff
	projectsDir := filepath.Join(aikitoDir, "projects")
	for _, name := range sortedDirEntries(projectsDir) {
		dir := filepath.Join(projectsDir, name)
		cfgPath := filepath.Join(dir, "agent.toml")
		if !isDir(dir) || !isRegularFile(cfgPath) {
			continue
		}
		raw, err := os.ReadFile(cfgPath)
		if err != nil {
			continue
		}
		cfg, err := workspace.DecodeTOML(raw)
		if err != nil || lowerStr(cfg["sync_mode"], "link") != "copy" {
			continue
		}
		binding := project.ResolveProjectBinding(cfg, home)
		var skills []string
		switch v := cfg["skills"].(type) {
		case []any:
			skills = listStrings(v)
		case string:
			for _, r := range v {
				skills = append(skills, string(r))
			}
		}
		sort.Strings(skills)
		for _, entry := range binding.ActiveEntries() {
			if projectFilter != nil && name != *projectFilter {
				continue
			}
			for _, skill := range skills {
				status, _ := copiedSkillState(aikitoDir, home, name, entry.ResolvedPath, skill)
				if status != "DRIFT" {
					continue
				}
				canonicalPath := filepath.Join(aikitoDir, "skills", skill)
				runtimePath := filepath.Join(entry.ResolvedPath, ".agents", "skills", skill)
				canonicalFiles, cerr := fileInventory(canonicalPath)
				runtimeFiles, rerr := fileInventory(runtimePath)
				if cerr != "" || rerr != "" {
					continue
				}
				keys := map[string]bool{}
				for k := range canonicalFiles {
					keys[k] = true
				}
				for k := range runtimeFiles {
					keys[k] = true
				}
				rels := make([]string, 0, len(keys))
				for k := range keys {
					rels = append(rels, k)
				}
				sort.Strings(rels)
				for _, rel := range rels {
					actualPath, hasActual := runtimeFiles[rel]
					expectedPath, hasExpected := canonicalFiles[rel]
					var actual, expected []byte
					if hasActual {
						if actual, err = os.ReadFile(actualPath); err != nil {
							continue
						}
					}
					if hasExpected {
						if expected, err = os.ReadFile(expectedPath); err != nil {
							continue
						}
					}
					if bytes.Equal(actual, expected) {
						continue
					}
					actualLabel, expectedLabel := "/dev/null", "/dev/null"
					if hasActual {
						actualLabel = actualPath
					}
					if hasExpected {
						expectedLabel = expectedPath
					}
					var diffText string
					if bytes.IndexByte(actual, 0) >= 0 || bytes.IndexByte(expected, 0) >= 0 {
						diffText = fmt.Sprintf("Binary files differ: %s and %s", actualLabel, expectedLabel)
					} else {
						diffText = unifiedDiff(
							splitKeepEnds(strings.ToValidUTF8(string(actual), "\uFFFD")),
							splitKeepEnds(strings.ToValidUTF8(string(expected), "\uFFFD")),
							"actual: "+actualLabel, "expected: "+expectedLabel)
					}
					results = append(results, driftDiff{kind: "project_skill", name: skill, diff: diffText,
						project: name, file: rel, checkout: entry.ResolvedPath})
				}
			}
		}
	}
	return results
}

// collectMCPDriftDiffs reuses the plan the MCP planner already computed
// (BuildMCPPlan performs exactly the same observed-entry read diff.py's
// read_entry call would do) rather than re-reading/re-parsing every agent
// config file independently in the CLI layer. An operation is diff-worthy
// when its action is "UPDATE" (a safe, non-conflicting change) or
// "CONFLICT" (mirrors evaluate_spec_status mapping both to "UPDATE"/"DRIFT")
// and it has an existing observed entry (diff.py skips a server whose
// current config entry is None — nothing to diff against yet).
func collectMCPDriftDiffs(aikitoDir, home string) ([]driftDiff, error) {
	plan, err := mcp.BuildMCPPlan(aikitoDir, home, mcp.BuildMCPPlanOptions{})
	if err != nil {
		// diff.py retries load_agent_specs, whose error (e.g. no mcps/
		// directory) is what the user sees.
		if _, specErr := mcp.LoadAgentSpecs(aikitoDir, home); specErr != nil {
			return nil, specErr
		}
		return nil, nil
	}
	var results []driftDiff
	for _, op := range plan.Operations {
		if op.Action != "UPDATE" && op.Action != "CONFLICT" {
			continue
		}
		if op.Observed == nil || !op.Observed.Exists {
			continue
		}
		actualLabel := op.Target.Path
		expectedLabel := fmt.Sprintf("mcps/%s.toml", op.Target.LogicalIdentity)
		actualJSON := mcpOrderedJSONSortedLines(op.Observed.Entry())
		expectedJSON := mcpOrderedJSONSortedLines(op.Desired.Desired())
		diffText := unifiedDiff(actualJSON, expectedJSON, "actual: "+actualLabel, "expected: "+expectedLabel)
		if diffText == "" {
			diffText = redactedOnlyDiff("actual: "+actualLabel, "expected: "+expectedLabel)
		}
		results = append(results, driftDiff{
			kind: "mcp", name: op.Target.LogicalIdentity, agent: op.Target.Agent, diff: diffText,
		})
	}
	return results, nil
}

// mcpOrderedJSONSortedLines mirrors diff.py's _json_lines: sorted-key,
// 2-space-indented JSON split into lines (keepends). This intentionally
// does NOT reuse mcp.DumpIndented, which preserves insertion order for
// file-writing purposes — the diff view sorts keys instead, matching
// Python's json.dumps(..., indent=2, sort_keys=True) exactly.
func mcpOrderedJSONSortedLines(v *mcp.OrderedObject) []string {
	var b strings.Builder
	writeSortedIndented(&b, v, "")
	return splitKeepEnds(b.String())
}

func writeSortedIndented(b *strings.Builder, v any, indent string) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case *mcp.OrderedObject:
		if x == nil || x.Len() == 0 {
			b.WriteString("{}")
			return
		}
		keys := x.Keys()
		sort.Strings(keys)
		child := indent + "  "
		b.WriteString("{\n")
		for i, k := range keys {
			b.WriteString(child)
			writeJSONStringPyLocal(b, k)
			b.WriteString(": ")
			val, _ := x.Get(k)
			writeSortedIndented(b, val, child)
			if i < len(keys)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(indent)
		b.WriteString("}")
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return
		}
		child := indent + "  "
		b.WriteString("[\n")
		for i, item := range x {
			b.WriteString(child)
			writeSortedIndented(b, item, child)
			if i < len(x)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(indent)
		b.WriteString("]")
	case string:
		writeJSONStringPyLocal(b, x)
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	default:
		fmt.Fprintf(b, "%v", x)
	}
}

func writeJSONStringPyLocal(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// collectSubagentDriftDiffs renders what each configured platform's native
// subagent file SHOULD contain (per internal/subagent's adapters) and
// diffs it against the actual on-disk content. Unlike the MCP path, there
// is no ported subagent sync *plan* builder to reuse (subagent.py's
// SubagentPlan depends on the unported config_runtime.py framework, per
// internal/subagent's own package doc) — this performs the render+compare
// directly, which is all a diff view needs (no CREATE/apply decisions).
//
// KNOWN DIVERGENCE from Python, found via cross-validation, not silently
// introduced: Python's collect_drift_diffs only shows a subagent as
// drifted when build_subagent_plan classifies its operation as "UPDATE" —
// which (per that plan's managed-fingerprint tracking, not ported here)
// requires the native file to have been previously *synced* by aikito at
// all. A hand-authored native file that was never synced is real content
// difference by this function's measure, but Python's real sync-plan-aware
// diff reports "no drift" for it (it isn't "managed" yet, so there's
// nothing to consider drifted). Confirmed directly: an identical fixture
// (a subagent added but never synced, with a hand-edited native file)
// shows a diff in this Go build and "No matching drift detected." in
// Python. This Go version is a reasonable, useful approximation — it
// still correctly reports clean content as no-drift, and genuinely-synced-
// then-drifted content as drift — but can be a false positive for a native
// file edited before ever being synced. Fully matching Python's behavior
// needs the managed-state tracking that a real subagent sync plan would
// carry, which is out of scope for this pass.
func collectSubagentDriftDiffs(aikitoDir, home string) []driftDiff {
	subagentsDir := aikitoDir + "/subagents"
	entries, err := os.ReadDir(subagentsDir)
	if err != nil {
		return nil
	}
	definitions, derr := registry.LoadAgentDefinitions(aikitoDir, home)
	if derr != nil {
		return nil
	}

	var results []driftDiff
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".md")
		metadata, body, perr := workspace.ParseSubagentFile(subagentsDir + "/" + entry.Name())
		if perr != nil {
			continue
		}
		description, _ := metadata["description"].(string)
		agentsRaw, _ := metadata["agents"].([]any)

		for _, a := range agentsRaw {
			platform, ok := a.(string)
			if !ok {
				continue
			}
			def, ok := definitions[platform]
			if !ok || def.Subagents == nil {
				continue
			}
			if def.Subagents.RequiresPath != nil {
				if _, serr := os.Stat(home + "/" + *def.Subagents.RequiresPath); serr != nil {
					continue
				}
			}
			adapter, aerr := subagent.GetSubagentAdapter(def.Subagents.ConfigFormat)
			if aerr != nil {
				continue
			}
			platformOpts, _ := metadata[platform].(map[string]any)

			// Python's subagent loader stores instructions=body.strip(), and
			// sync renders from that (internal/sync/subagents.go does the
			// same); rendering the raw body here made every freshly-synced
			// subagent show a phantom trailing-newline drift.
			rendered, rerr := adapter.Render(name, description, platformOpts, strings.TrimSpace(body))
			if rerr != nil {
				continue
			}

			// ResolveTargetPath's "root" is the agent's Subagents.ConfigPath
			// (a directory for per_file layouts like claude_markdown's
			// "<root>/<name>.md", or the single shared file itself for
			// DSH's shared_patch layout) — registry.LoadAgentDefinitions
			// already resolves ConfigPath to an absolute path, so it's used
			// directly, not re-joined onto home.
			root := def.Subagents.ConfigPath
			targetPath := adapter.ResolveTargetPath(root, name)
			data, rferr := os.ReadFile(targetPath)
			if rferr != nil {
				continue // nothing on disk yet: CREATE, not drift — nothing to diff
			}

			var actual, expected string
			if adapter.Layout == subagent.LayoutSharedPatch {
				current, found := adapter.ReadItem(string(data), name)
				if !found {
					continue
				}
				actual, expected = current, rendered
			} else {
				actual, expected = string(data), rendered
			}

			diffText := unifiedDiff(
				splitKeepEnds(actual), splitKeepEnds(expected),
				targetPath, fmt.Sprintf("subagents/%s.md (%s)", name, platform),
			)
			if diffText == "" {
				continue
			}
			results = append(results, driftDiff{kind: "subagent", name: name, agent: platform, diff: diffText})
		}
	}
	return results
}

func redactedOnlyDiff(actualLabel, expectedLabel string) string {
	return unifiedDiff(
		[]string{"<redacted value differs>\n"},
		[]string{"<expected redacted value>\n"},
		actualLabel, expectedLabel,
	)
}

// --- rendering (diff.py's render_drift_index / render_drift_diffs) ---

func renderDriftDiffs(diffs []driftDiff, emptyMessage string) string {
	if len(diffs) == 0 {
		return emptyMessage
	}
	parts := make([]string, len(diffs))
	for i, d := range diffs {
		parts[i] = fmt.Sprintf("[%s]\n%s", d.displayLabel(), d.diff)
	}
	return strings.Join(parts, "\n\n")
}

func renderDriftIndex(diffs []driftDiff) string {
	if len(diffs) == 0 {
		return "No drift detected."
	}
	var lines []string
	lines = append(lines, "Drift detected:")

	var mcpItems, subagentItems, projectItems []driftDiff
	for _, d := range diffs {
		switch {
		case d.kind == "mcp":
			mcpItems = append(mcpItems, d)
		case d.kind == "subagent":
			subagentItems = append(subagentItems, d)
		case d.kind == "project_skill" && d.project != "":
			projectItems = append(projectItems, d)
		}
	}

	if len(mcpItems) > 0 {
		sort.Slice(mcpItems, func(i, j int) bool {
			return mcpItems[i].agent+"/"+mcpItems[i].name < mcpItems[j].agent+"/"+mcpItems[j].name
		})
		lines = append(lines, "", "MCP")
		for _, d := range mcpItems {
			lines = append(lines, fmt.Sprintf("  %s/%s", d.agent, d.name))
		}
	}
	if len(subagentItems) > 0 {
		sort.Slice(subagentItems, func(i, j int) bool {
			return subagentItems[i].agent+"/"+subagentItems[i].name < subagentItems[j].agent+"/"+subagentItems[j].name
		})
		lines = append(lines, "", "Subagents")
		for _, d := range subagentItems {
			lines = append(lines, fmt.Sprintf("  %s/%s", d.agent, d.name))
		}
	}

	if len(projectItems) > 0 {
		lines = append(lines, "", "Projects")
		counts := map[string]map[string]map[string]int{}
		for _, d := range projectItems {
			if counts[d.project] == nil {
				counts[d.project] = map[string]map[string]int{}
			}
			if counts[d.project][d.checkout] == nil {
				counts[d.project][d.checkout] = map[string]int{}
			}
			counts[d.project][d.checkout][d.name]++
		}
		for n, proj := range sortedKeys3(counts) {
			if n > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, "  "+proj)
			for _, checkout := range sortedKeys2(counts[proj]) {
				indent := "    "
				if checkout != "" {
					lines = append(lines, "    Checkout: "+checkout)
					indent = "      "
				}
				skills := counts[proj][checkout]
				for _, skill := range sortedKeys1(skills) {
					plural := "files"
					if skills[skill] == 1 {
						plural = "file"
					}
					lines = append(lines, fmt.Sprintf("%s%-14s %d %s changed", indent, skill, skills[skill], plural))
				}
			}
		}
	}

	var hints []string
	if len(projectItems) > 0 {
		hints = append(hints, "aikito diff project <project> <skill>")
	}
	if len(mcpItems) > 0 {
		hints = append(hints, "aikito diff mcp <agent> <server>")
	}
	if len(subagentItems) > 0 {
		hints = append(hints, "aikito diff subagent <agent> <name>")
	}
	if len(hints) > 0 {
		lines = append(lines, "", "Review details:")
		for _, h := range hints {
			lines = append(lines, "  "+h)
		}
	}
	return strings.Join(lines, "\n")
}

func sortedKeys1(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys2(m map[string]map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys3(m map[string]map[string]map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// renderProjectDriftIndex is diff.py's render_project_drift_index.
func renderProjectDriftIndex(projectName string, diffs []driftDiff) string {
	checkouts := map[string]map[string][]string{}
	matched := false
	for _, d := range diffs {
		if d.kind != "project_skill" || d.project != projectName {
			continue
		}
		matched = true
		if d.file == "" {
			continue
		}
		if checkouts[d.checkout] == nil {
			checkouts[d.checkout] = map[string][]string{}
		}
		checkouts[d.checkout][d.name] = append(checkouts[d.checkout][d.name], d.file)
	}
	if !matched {
		return "No matching drift detected."
	}
	lines := []string{"Project " + projectName}
	cos := make([]string, 0, len(checkouts))
	for c := range checkouts {
		cos = append(cos, c)
	}
	sort.Strings(cos)
	for _, c := range cos {
		if c != "" {
			lines = append(lines, "", "Checkout: "+c)
		}
		skills := make([]string, 0, len(checkouts[c]))
		for k := range checkouts[c] {
			skills = append(skills, k)
		}
		sort.Strings(skills)
		for _, skill := range skills {
			lines = append(lines, "", skill)
			files := append([]string{}, checkouts[c][skill]...)
			sort.Strings(files)
			for _, f := range files {
				lines = append(lines, "  "+f)
			}
		}
	}
	lines = append(lines, "", "Review details:", "  aikito diff project "+projectName+" <skill>")
	return strings.Join(lines, "\n")
}
