package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mr-miles/aikito-go/internal/linkplan"
	"github.com/mr-miles/aikito-go/internal/mcp"
	"github.com/mr-miles/aikito-go/internal/registry"
	"github.com/mr-miles/aikito-go/internal/subagent"
	"github.com/mr-miles/aikito-go/internal/sync"
)

// resolveDetailName is status.py _resolve_name: exact name, else a unique
// prefix.
func resolveDetailName(target string, names []string, resource string) (string, error) {
	for _, n := range names {
		if n == target {
			return n, nil
		}
	}
	var matches []string
	for _, n := range names {
		if strings.HasPrefix(n, target) {
			matches = append(matches, n)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("Unknown %s '%s'; available: %s", resource, target, strings.Join(names, ", "))
	}
	return "", fmt.Errorf("Ambiguous %s '%s'; matches: %s", resource, target, strings.Join(matches, ", "))
}

// --- MCP ---

type mcpDetailRow struct {
	serverName, targetName, agentName, agentDisplayName string
	source, status, configPath, configFormat            string
	entry                                               *mcp.OrderedObject
}

// collectMCPDetails ports status.py collect_mcp_details.
func collectMCPDetails(c *inspectionContext, serverTarget, agentTarget string) ([]mcpDetailRow, error) {
	_, defs, err := c.agents()
	if err != nil {
		return nil, err
	}
	specs, serr := c.mcp()
	if serr != nil {
		specs = nil
	}
	serverSet := map[string]bool{}
	for _, s := range specs {
		if s.Enabled {
			serverSet[s.Server] = true
		}
	}
	serverNames := sortedKeys(serverSet)
	serverName, agentName := "", ""
	if serverTarget != "" {
		if serverName, err = resolveDetailName(serverTarget, serverNames, "MCP server"); err != nil {
			return nil, err
		}
	}
	if agentTarget != "" {
		names := make([]string, 0, len(defs))
		for n := range defs {
			names = append(names, n)
		}
		sort.Strings(names)
		if agentName, err = resolveDetailName(agentTarget, names, "agent"); err != nil {
			return nil, err
		}
	}

	var rows []mcpDetailRow
	managed := map[string]map[string]bool{}
	for _, spec := range specs {
		if !spec.Enabled || (serverName != "" && spec.Server != serverName) || (agentName != "" && spec.Agent != agentName) {
			continue
		}
		var current *mcp.OrderedObject
		if isRegularFilePath(spec.ConfigPath) {
			data, rerr := os.ReadFile(spec.ConfigPath)
			if rerr != nil {
				return nil, rerr
			}
			if current, err = mcp.ReadEntry(spec, string(data)); err != nil {
				return nil, err
			}
		}
		var entry *mcp.OrderedObject
		if current != nil {
			entry = mcp.RedactMCPEntry(current)
		}
		rows = append(rows, mcpDetailRow{
			serverName: spec.Server, targetName: spec.TargetName, agentName: spec.Agent,
			agentDisplayName: defs[spec.Agent].DisplayName, source: "managed", status: c.mcpStatus(spec),
			configPath: spec.ConfigPath, configFormat: spec.ConfigFormat, entry: entry,
		})
		if managed[spec.Agent] == nil {
			managed[spec.Agent] = map[string]bool{}
		}
		managed[spec.Agent][spec.TargetName] = true
	}

	if agentName != "" && serverName == "" {
		def := defs[agentName]
		if capability := def.MCP; capability != nil && capability.ConfigPath != "" && isRegularFilePath(capability.ConfigPath) {
			data, rerr := os.ReadFile(capability.ConfigPath)
			if rerr != nil {
				return nil, rerr
			}
			entries, eerr := mcp.ReadAllEntries(capability.Adapter, string(data))
			if eerr != nil {
				return nil, eerr
			}
			var unmanaged []string
			if entries != nil {
				for _, k := range entries.Keys() {
					if !managed[agentName][k] {
						unmanaged = append(unmanaged, k)
					}
				}
			}
			sort.Strings(unmanaged)
			for _, name := range unmanaged {
				rows = append(rows, mcpDetailRow{
					serverName: name, targetName: name, agentName: agentName, agentDisplayName: def.DisplayName,
					source: "unmanaged", status: "PRESENT", configPath: capability.ConfigPath,
					configFormat: capability.ConfigFormat,
				})
			}
		}
	}
	return rows, nil
}

func detailStatusText(status string) string {
	if status == "OK" {
		return "synced"
	}
	return strings.ToLower(status)
}

// renderMCPDetails ports render.py render_mcp_details.
func renderMCPDetails(rows []mcpDetailRow, canonicalPath string, useUnicode, useColor bool) string {
	overview := renderKeyValueFields([][2]string{{"MCP Server:", rows[0].serverName}, {"Canonical source:", canonicalPath}})
	var blocks []string
	for _, r := range rows {
		entry := "<missing>"
		if r.entry != nil {
			entry = mcp.DumpIndented(r.entry)
		}
		blocks = append(blocks, strings.Join([]string{
			renderTitleBox(r.agentDisplayName, useUnicode, useColor, 0),
			renderKeyValueFields([][2]string{
				{"Agent key:", r.agentName}, {"Status:", detailStatusText(r.status)},
				{"Config:", r.configPath}, {"Format:", r.configFormat},
			}),
			"", "Managed entry:", entry,
		}, "\n"))
	}
	return overview + "\n\n" + strings.Join(blocks, "\n\n")
}

// renderAgentMCPTable ports render.py render_agent_mcp_table.
func renderAgentMCPTable(rows []mcpDetailRow, useUnicode, useColor bool) string {
	var table [][]string
	issue := false
	for _, r := range rows {
		table = append(table, []string{r.serverName, strings.ToUpper(r.source[:1]) + r.source[1:], formatStatusBadge(r.status, useUnicode, useColor)})
		issue = issue || badgeIsIssue(r.status, useUnicode)
	}
	out := buildGenericTable([]string{"MCP Server", "Source", "Status"}, table, useUnicode, useColor, nil)
	if issue {
		out += "\n\n" + renderLegend(useUnicode, useColor)
	}
	return out
}

// showMCPAgentDetails is the --agent branch of cli_show.py cmd_show_mcp.
func showMCPAgentDetails(sa showArgs, aikitoDir string, env Environment, stdout, stderr io.Writer) int {
	agentTarget := sa.agent
	rows, err := collectMCPDetails(newInspectionContext(aikitoDir, env.Home), sa.target, agentTarget)
	if err == nil && len(rows) == 0 {
		var parts []string
		for _, v := range []string{agentTarget, sa.target} {
			if v != "" {
				parts = append(parts, v)
			}
		}
		err = fmt.Errorf("No MCP configuration found for '%s'", strings.Join(parts, "/"))
	}
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if agentTarget != "" && sa.target == "" {
		first := rows[0]
		var nManaged, synced, drifted, unmanaged int
		for _, r := range rows {
			if r.source == "managed" {
				nManaged++
				if r.status == "OK" {
					synced++
				}
				if r.status == "DRIFT" {
					drifted++
				}
			} else if r.source == "unmanaged" {
				unmanaged++
			}
		}
		fmt.Fprintln(stdout, renderKeyValueFields([][2]string{
			{"Agent:", first.agentDisplayName}, {"Agent key:", first.agentName},
			{"Config:", first.configPath}, {"Format:", first.configFormat},
			{"MCP Servers:", fmt.Sprintf("%d managed, %d synced, %d drifted, %d unmanaged", nManaged, synced, drifted, unmanaged)},
		}))
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, renderAgentMCPTable(rows, sa.useUnicode, sa.useColor))
		return 0
	}
	fmt.Fprintln(stdout, renderMCPDetails(rows, filepath.Join(aikitoDir, "mcps", rows[0].serverName+".toml"), sa.useUnicode, sa.useColor))
	return 0
}

// --- subagents ---

type subagentDetailRow struct {
	subagentName, description, agentName, agentDisplayName string
	status, targetPath, configFormat, canonicalPath        string
	platformOptions                                        map[string]any
}

// collectSubagentDetails ports status.py collect_subagent_details.
func collectSubagentDetails(c *inspectionContext, subagentTarget, agentTarget string) ([]subagentDetailRow, error) {
	_, defs, err := c.agents()
	if err != nil {
		return nil, err
	}
	type agentCfg struct {
		name, display, configPath, configFormat string
		adapter                                 subagent.Adapter
	}
	configs := map[string]agentCfg{}
	for name, d := range defs {
		if d.Subagents == nil {
			continue
		}
		adapter, aerr := subagent.GetSubagentAdapter(d.Subagents.ConfigFormat)
		if aerr != nil {
			return nil, fmt.Errorf("Agent '%s' unsupported config_format '%s'", name, d.Subagents.ConfigFormat)
		}
		configs[name] = agentCfg{name, d.DisplayName, d.Subagents.ConfigPath, d.Subagents.ConfigFormat, adapter}
	}
	subDefs, err := sync.LoadSubagentDefinitions(c.aikitoDir)
	if err != nil {
		return nil, err
	}
	views, _, verr := c.subagentViews()
	if verr != nil {
		views = nil
	}
	subNames := make([]string, 0, len(subDefs))
	for n := range subDefs {
		subNames = append(subNames, n)
	}
	sort.Strings(subNames)
	subName, agentName := "", ""
	if subagentTarget != "" {
		if subName, err = resolveDetailName(subagentTarget, subNames, "subagent"); err != nil {
			return nil, err
		}
	}
	if agentTarget != "" {
		names := make([]string, 0, len(configs))
		for n := range configs {
			names = append(names, n)
		}
		sort.Strings(names)
		if agentName, err = resolveDetailName(agentTarget, names, "agent"); err != nil {
			return nil, err
		}
	}
	byKey := map[[2]string]linkplan.View{}
	for _, v := range views {
		byKey[[2]string{v.ResourceName, v.Agent}] = v
	}
	agentKeys := make([]string, 0, len(configs))
	for n := range configs {
		agentKeys = append(agentKeys, n)
	}
	sort.Strings(agentKeys)

	var rows []subagentDetailRow
	for _, name := range subNames {
		if subName != "" && name != subName {
			continue
		}
		def := subDefs[name]
		targeted := map[string]bool{}
		for _, a := range def.Agents {
			targeted[a] = true
		}
		for _, key := range agentKeys {
			if agentName != "" && key != agentName {
				continue
			}
			if !targeted[key] && !(subName != "" && agentName != "") {
				continue
			}
			cfg := configs[key]
			var status, target string
			v, hasView := byKey[[2]string{name, key}]
			switch {
			case !targeted[key]:
				status, target = "NOT_TARGETED", cfg.adapter.ResolveTargetPath(cfg.configPath, name)
			case hasView:
				status = v.Status
				if status == linkplan.StatusUpdate {
					status = "DRIFT"
				}
				target = v.TargetPath
				if target == "" {
					target = cfg.adapter.ResolveTargetPath(cfg.configPath, name)
				}
			default:
				status, target = "MISSING", cfg.adapter.ResolveTargetPath(cfg.configPath, name)
			}
			rows = append(rows, subagentDetailRow{
				subagentName: name, description: def.Description, agentName: key, agentDisplayName: cfg.display,
				status: status, targetPath: target, configFormat: cfg.configFormat,
				canonicalPath:   filepath.Join(c.aikitoDir, "subagents", name+".md"),
				platformOptions: def.PlatformConfigs[key],
			})
		}
	}
	return rows, nil
}

// pyStrValue is Python's str() of a decoded TOML value.
func pyStrValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return pyRepr(v)
}

func sortedOptionKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// renderAgentSubagentTable ports render.py render_agent_subagent_table.
func renderAgentSubagentTable(rows []subagentDetailRow, useUnicode, useColor bool) string {
	var table [][]string
	issue := false
	for _, r := range rows {
		opts := "(defaults)"
		if len(r.platformOptions) > 0 {
			var parts []string
			for _, k := range sortedOptionKeys(r.platformOptions) {
				parts = append(parts, k+": "+pyStrValue(r.platformOptions[k]))
			}
			opts = strings.Join(parts, ", ")
		}
		table = append(table, []string{r.subagentName, formatStatusBadge(r.status, useUnicode, useColor), opts, r.description})
		issue = issue || badgeIsIssue(r.status, useUnicode)
	}
	out := buildGenericTable([]string{"Subagent", "Status", "Overrides", "Description"}, table, useUnicode, useColor, nil)
	if issue {
		out += "\n\n" + renderLegend(useUnicode, useColor)
	}
	return out
}

// renderSubagentDetails ports render.py render_subagent_details.
func renderSubagentDetails(rows []subagentDetailRow, canonicalPath string, useUnicode, useColor bool) string {
	first := rows[0]
	overview := renderKeyValueFields([][2]string{
		{"Subagent:", first.subagentName}, {"Description:", first.description}, {"Canonical source:", canonicalPath},
	})
	var blocks []string
	for _, r := range rows {
		var statusText, opts string
		if r.status == "NOT_TARGETED" {
			statusText, opts = "not targeted by this subagent", "n/a (not targeted by this subagent)"
		} else {
			statusText = detailStatusText(r.status)
			opts = "(defaults)"
			if len(r.platformOptions) > 0 {
				var fields [][2]string
				for _, k := range sortedOptionKeys(r.platformOptions) {
					fields = append(fields, [2]string{k + ":", pyStrValue(r.platformOptions[k])})
				}
				opts = renderKeyValueFields(fields)
			}
		}
		blocks = append(blocks, strings.Join([]string{
			renderTitleBox(r.agentDisplayName, useUnicode, useColor, 0),
			renderKeyValueFields([][2]string{
				{"Agent key:", r.agentName}, {"Status:", statusText},
				{"Target:", r.targetPath}, {"Format:", r.configFormat},
			}),
			"", "Platform options:", opts,
		}, "\n"))
	}
	return overview + "\n\n" + strings.Join(blocks, "\n\n")
}

// showSubagentAgentDetails is the --agent branch of cmd_show_subagents.
func showSubagentAgentDetails(sa showArgs, aikitoDir string, env Environment, stdout, stderr io.Writer) int {
	agentTarget := sa.agent
	rows, err := collectSubagentDetails(newInspectionContext(aikitoDir, env.Home), sa.target, agentTarget)
	if err == nil && len(rows) == 0 {
		var parts []string
		for _, v := range []string{agentTarget, sa.target} {
			if v != "" {
				parts = append(parts, v)
			}
		}
		err = fmt.Errorf("No subagent configuration found for '%s'", strings.Join(parts, "/"))
	}
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if agentTarget != "" && sa.target == "" {
		first := rows[0]
		var synced, drifted, missing int
		for _, r := range rows {
			switch r.status {
			case "OK":
				synced++
			case "DRIFT":
				drifted++
			case "MISSING":
				missing++
			}
		}
		fmt.Fprintln(stdout, renderKeyValueFields([][2]string{
			{"Agent:", first.agentDisplayName}, {"Agent key:", first.agentName},
			{"Target dir:", filepath.Dir(first.targetPath)}, {"Format:", first.configFormat},
			{"Subagents:", fmt.Sprintf("%d managed, %d synced, %d drifted, %d missing", len(rows), synced, drifted, missing)},
		}))
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, renderAgentSubagentTable(rows, sa.useUnicode, sa.useColor))
		return 0
	}
	fmt.Fprintln(stdout, renderSubagentDetails(rows, filepath.Join(aikitoDir, "subagents", rows[0].subagentName+".md"), sa.useUnicode, sa.useColor))
	return 0
}

// --- live MCP probes ---

// mcpProbeTimeout is probe_mcp_tools_for_specs' default timeout.
const mcpProbeTimeout = 15 * time.Second

// showMCPLive is the `show mcp <target> --live` branch of cmd_show_mcp
// (collect_mcp_runtime + render_mcp_runtime_table). Python's animated
// "loading" line on a terminal's stderr is not reproduced.
func showMCPLive(sa showArgs, aikitoDir string, env Environment, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	defs, err := registry.LoadAgentDefinitions(aikitoDir, env.Home)
	if err != nil {
		return fail(err)
	}
	specs, err := mcp.LoadAgentSpecs(aikitoDir, env.Home)
	if err != nil {
		return fail(err)
	}
	serverSet := map[string]bool{}
	for _, s := range specs {
		if s.Enabled {
			serverSet[s.Server] = true
		}
	}
	serverName, err := resolveDetailName(sa.target, sortedKeys(serverSet), "MCP server")
	if err != nil {
		return fail(err)
	}
	agentName := ""
	if sa.agent != "" {
		names := make([]string, 0, len(defs))
		for n := range defs {
			names = append(names, n)
		}
		sort.Strings(names)
		if agentName, err = resolveDetailName(sa.agent, names, "agent"); err != nil {
			return fail(err)
		}
	}
	var selected []mcp.AgentSpec
	for _, s := range specs {
		if s.Enabled && s.Server == serverName && (agentName == "" || s.Agent == agentName) {
			selected = append(selected, s)
		}
	}
	if len(selected) == 0 {
		selection := serverName
		if agentName != "" {
			selection = serverName + "/" + agentName
		}
		return fail(fmt.Errorf("No MCP configuration found for '%s'", selection))
	}
	results := mcp.ProbeMCPToolsForSpecs(selected, mcpProbeTimeout)

	fmt.Fprintln(stdout, renderKeyValueFields([][2]string{
		{"MCP Server:", serverName},
		{"Canonical source:", filepath.Join(aikitoDir, "mcps", serverName+".toml")},
	}))
	fmt.Fprintln(stdout)
	unavailable := "-"
	if sa.useUnicode {
		unavailable = "–"
	}
	var table [][]string
	issue := false
	for _, r := range results {
		tools := unavailable
		if r.Status == "OK" {
			tools = fmt.Sprint(len(r.ToolNames))
		}
		table = append(table, []string{defs[r.Agent].DisplayName, formatStatusBadge(r.Status, sa.useUnicode, sa.useColor), r.AuthMethod, tools})
		issue = issue || badgeIsIssue(r.Status, sa.useUnicode)
	}
	out := buildGenericTable([]string{"Agent", "Connect", "Auth method", "Tools"}, table, sa.useUnicode, sa.useColor, nil)
	if issue {
		out += "\n\n" + renderLegend(sa.useUnicode, sa.useColor)
	}
	fmt.Fprintln(stdout, out)

	printedGap := false
	for _, r := range results {
		if r.Error == "" {
			continue
		}
		if !printedGap {
			fmt.Fprintln(stdout)
			printedGap = true
		}
		fmt.Fprintf(stdout, "[%s] %s: %s\n", r.Status, defs[r.Agent].DisplayName, r.Error)
	}
	if sa.agentHasValue && len(results) == 1 && len(results[0].ToolNames) > 0 {
		fmt.Fprintln(stdout)
		fmt.Fprintf(stdout, "Tools (%d):\n", len(results[0].ToolNames))
		for _, t := range results[0].ToolNames {
			fmt.Fprintf(stdout, "- %s\n", t)
		}
	}
	return 0
}
