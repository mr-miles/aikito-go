package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-rs/internal/mcp"
	"github.com/mr-miles/aikito-rs/internal/registry"
	"github.com/mr-miles/aikito-rs/internal/subagent"
	"github.com/mr-miles/aikito-rs/internal/workspace"
)

// driftDiff mirrors diff_model.py's DriftDiff: a structured, machine-
// filterable identity plus a pre-rendered unified-diff string.
type driftDiff struct {
	kind  string // "mcp" | "subagent" | "project_skill" (not yet populated)
	name  string
	agent string
	diff  string
}

func (d driftDiff) displayLabel() string {
	switch d.kind {
	case "mcp":
		return fmt.Sprintf("MCP %s/%s", d.agent, d.name)
	case "subagent":
		return fmt.Sprintf("Subagent %s/%s", d.agent, d.name)
	default:
		return d.name
	}
}

// cmdDiff mirrors cli.py's cmd_diff.
//
// Scope note: ports the "mcp" and "subagent" diff targets in full (they
// only need resources already built: the MCP planner and the subagent
// per-platform renderers). The "project" target (project-skill copy-mode
// drift, collect_project_skill_diffs in project.py) needs project_sync.py's
// skill-materialization machinery, not yet ported in this Go build (a
// parallel, not-yet-landed "sync project" effort would be the natural home
// for it) — it prints a clear not-yet-implemented notice instead of
// guessing at a different semantic.
func cmdDiff(args []string, stdout, stderr io.Writer, env Environment) int {
	all := false
	var rest []string
	for _, a := range args {
		if a == "--all" {
			all = true
			continue
		}
		rest = append(rest, a)
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

	if all {
		diffs := collectDriftDiffs(aikitoDir, env.Home, "")
		fmt.Fprintln(stdout, renderDriftDiffs(diffs, "No drift detected."))
		return 0
	}

	if len(rest) > 0 {
		switch rest[0] {
		case "project":
			fmt.Fprintln(stderr, "[ERROR] 'aikito diff project' is not yet implemented in this Go build "+
				"(needs project skill copy-mode sync, not yet ported). Use 'aikito diff mcp'/'aikito diff subagent' for now.")
			return 2
		case "mcp":
			if len(rest) < 3 {
				fmt.Fprintln(stderr, "[ERROR] Usage: aikito diff mcp <agent> <server>")
				return 2
			}
			diffs := collectDriftDiffs(aikitoDir, env.Home, "mcp")
			matching := filterDriftDiffs(diffs, "mcp", rest[1], rest[2])
			fmt.Fprintln(stdout, renderDriftDiffs(matching, "No matching drift detected."))
			return 0
		case "subagent":
			if len(rest) < 3 {
				fmt.Fprintln(stderr, "[ERROR] Usage: aikito diff subagent <agent> <name>")
				return 2
			}
			diffs := collectDriftDiffs(aikitoDir, env.Home, "subagent")
			matching := filterDriftDiffs(diffs, "subagent", rest[1], rest[2])
			fmt.Fprintln(stdout, renderDriftDiffs(matching, "No matching drift detected."))
			return 0
		default:
			fmt.Fprintf(stderr, "[ERROR] Unknown diff target: %s\n", rest[0])
			return 2
		}
	}

	diffs := collectDriftDiffs(aikitoDir, env.Home, "")
	fmt.Fprintln(stdout, renderDriftIndex(diffs))
	return 0
}

func filterDriftDiffs(diffs []driftDiff, kind, agent, name string) []driftDiff {
	var out []driftDiff
	for _, d := range diffs {
		if d.kind != kind {
			continue
		}
		if agent != "" && d.agent != agent {
			continue
		}
		if name != "" && d.name != name {
			continue
		}
		out = append(out, d)
	}
	return out
}

// collectDriftDiffs mirrors diff.py's collect_drift_diffs for the "mcp" and
// "subagent" kinds (kind == "" collects both; project_skill is out of
// scope, see cmdDiff's doc comment).
func collectDriftDiffs(aikitoDir, home, kind string) []driftDiff {
	var results []driftDiff
	if kind == "" || kind == "mcp" {
		results = append(results, collectMCPDriftDiffs(aikitoDir, home)...)
	}
	if kind == "" || kind == "subagent" {
		results = append(results, collectSubagentDriftDiffs(aikitoDir, home)...)
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
func collectMCPDriftDiffs(aikitoDir, home string) []driftDiff {
	plan, err := mcp.BuildMCPPlan(aikitoDir, home, mcp.BuildMCPPlanOptions{})
	if err != nil {
		return nil
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
	return results
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

	var mcpItems, subagentItems []driftDiff
	for _, d := range diffs {
		switch d.kind {
		case "mcp":
			mcpItems = append(mcpItems, d)
		case "subagent":
			subagentItems = append(subagentItems, d)
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

	var hints []string
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
