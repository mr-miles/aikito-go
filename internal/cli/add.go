package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mr-miles/aikito-go/internal/registry"
	"github.com/mr-miles/aikito-go/internal/subagent"
	"github.com/mr-miles/aikito-go/internal/sync"
	"github.com/mr-miles/aikito-go/internal/workspace"
)

// checkWorkspaceInitialized mirrors add.py's _check_workspace_initialized.
func checkWorkspaceInitialized(aikitoDir string) string {
	info, err := os.Stat(aikitoDir)
	if err != nil || !info.IsDir() {
		return fmt.Sprintf("Aikito workspace directory not found: %s", aikitoDir)
	}
	if fi, err := os.Stat(filepath.Join(aikitoDir, "layout.toml")); err != nil || !fi.Mode().IsRegular() {
		return fmt.Sprintf("Aikito workspace is not initialized at: %s", aikitoDir)
	}
	return ""
}

// titleize mirrors add.py's _titleize: kebab/snake-case -> Title Case.
func titleize(name string) string {
	replaced := strings.NewReplacer("-", " ", "_", " ").Replace(name)
	fields := strings.Fields(replaced)
	for i, w := range fields {
		if w == "" {
			continue
		}
		r := []rune(w)
		fields[i] = strings.ToUpper(string(r[0])) + strings.ToLower(string(r[1:]))
	}
	return strings.Join(fields, " ")
}

func cmdAdd(args []string, stdout, stderr io.Writer, env Environment) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: aikito add skill|subagent|mcp ...")
		return 2
	}
	switch args[0] {
	case "skill", "skills":
		return cmdAddSkill(args[1:], stdout, stderr, env)
	case "subagent", "subagents":
		return cmdAddSubagent(args[1:], stdout, stderr, env)
	case "mcp", "mcps":
		return cmdAddMCP(args[1:], stdout, stderr, env)
	default:
		fmt.Fprintf(stderr, "[ERROR] Unknown add target: %s\n", args[0])
		return 2
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func stringListEquals(raw []any, want []string) bool {
	if len(raw) != len(want) {
		return false
	}
	for i, v := range raw {
		s, ok := v.(string)
		if !ok || s != want[i] {
			return false
		}
	}
	return true
}

// parseSimpleMarkdownFrontmatter is a simplified stand-in for
// frontmatter.py's _parse_markdown_frontmatter: handles the common case of
// "---\nkey: plain scalar value\n---\nbody", stripping surrounding quotes
// from quoted values. Does NOT implement YAML block scalars (|, >), nested
// lists/maps, or multi-line values — real-world SKILL.md frontmatter
// overwhelmingly uses simple name/description scalars, and this port's
// scope/time budget didn't extend to a full YAML-subset parser. If content
// has no "---" frontmatter block at all, returns an empty map and the whole
// trimmed content as body.
func parseSimpleMarkdownFrontmatter(content string) (map[string]any, string) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return map[string]any{}, strings.TrimSpace(content)
	}
	closing := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			closing = i
			break
		}
	}
	if closing == -1 {
		return map[string]any{}, strings.TrimSpace(content)
	}
	meta := map[string]any{}
	for _, line := range lines[1:closing] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		val = strings.Trim(val, `"'`)
		meta[key] = val
	}
	body := strings.Join(lines[closing+1:], "\n")
	return meta, strings.TrimLeft(body, "\n")
}

// --- add subagent ---
//
// Scope note: ports add_subagent's core (add.py:1070-1205) EXCEPT --from
// import (_resolve_subagent_source's file/dir disambiguation heuristics,
// add.py:887-1044) and --sync (needs sync_subagent_configs, not yet
// built) — both print a clear error/TODO rather than silently no-op.
func cmdAddSubagent(args []string, stdout, stderr io.Writer, env Environment) int {
	var name, description, agentsArg string
	var force, syncFlag, fromGiven bool
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--from":
			fromGiven = true
			i++ // consume value even though unsupported, for a clean error below
		case a == "--description":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "[ERROR] --description requires a value")
				return 2
			}
			description = args[i]
		case a == "--agents":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "[ERROR] --agents requires a value")
				return 2
			}
			agentsArg = args[i]
		case a == "--force":
			force = true
		case a == "--sync":
			syncFlag = true
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(stderr, "[ERROR] Unknown flag: %s\n", a)
			return 2
		default:
			positional = append(positional, a)
		}
	}
	if fromGiven {
		fmt.Fprintln(stderr, "[ERROR] --from (importing a subagent from an external file) is not yet implemented in this Go build.")
		return 2
	}
	if len(positional) > 0 {
		name = positional[0]
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
	if force {
		fmt.Fprintln(stderr, "[ERROR] --force requires --from when adding a subagent.")
		return 1
	}

	nameClean := strings.TrimSpace(name)
	if msg := workspace.ValidateResourceName(nameClean, "subagent"); msg != "" {
		fmt.Fprintf(stderr, "[ERROR] %s\n", msg)
		return 1
	}

	path := filepath.Join(aikitoDir, "subagents", nameClean+".md")
	exists := false
	if info, serr := os.Lstat(path); serr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			fmt.Fprintf(stderr, "[ERROR] Unsafe subagent file: %s\n", path)
			return 1
		}
		exists = true
		if !force {
			fmt.Fprintf(stderr, "[ERROR] Subagent '%s' is already registered.\n", nameClean)
			return 1
		}
	}

	var oldMetadata map[string]any
	if exists {
		meta, _, perr := workspace.ParseSubagentFile(path)
		if perr != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", perr)
			return 1
		}
		oldMetadata = meta
	}

	var targetAgents []string
	if agentsArg != "" {
		for _, a := range strings.Split(agentsArg, ",") {
			if t := strings.TrimSpace(a); t != "" {
				targetAgents = append(targetAgents, t)
			}
		}
	}
	if len(targetAgents) == 0 && oldMetadata != nil {
		if raw, ok := oldMetadata["agents"].([]any); ok {
			for _, v := range raw {
				if s, ok := v.(string); ok {
					targetAgents = append(targetAgents, s)
				}
			}
		}
	}
	if len(targetAgents) == 0 {
		targetAgents = append([]string{}, defaultSubagentAgents...)
	}

	desc := strings.TrimSpace(description)
	if desc == "" && oldMetadata != nil {
		if d, ok := oldMetadata["description"].(string); ok {
			desc = d
		}
	}
	if desc == "" {
		desc = fmt.Sprintf("Subagent %s.", nameClean)
	}

	platformConfigs := map[string]any{}
	for k, v := range oldMetadata {
		if k == "description" || k == "agents" {
			continue
		}
		if m, ok := v.(map[string]any); ok {
			platformConfigs[k] = m
		}
	}

	definitions, derr := registry.LoadAgentDefinitions(aikitoDir, env.Home)
	if derr != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", derr)
		return 1
	}
	for platform, opts := range platformConfigs {
		optsMap, _ := opts.(map[string]any)
		def, ok := definitions[platform]
		if !ok || def.Subagents == nil {
			continue // matches Python: platforms without a definition stay portable, untouched
		}
		adapter, aerr := subagent.GetSubagentAdapter(def.Subagents.ConfigFormat)
		if aerr != nil {
			continue
		}
		if _, verr := adapter.ValidateOptions(platform, nameClean, optsMap); verr != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", verr)
			return 1
		}
	}

	body := fmt.Sprintf("# %s\n\nAdd developer instructions for the %s subagent here.\n", titleize(nameClean), nameClean)

	metadata := map[string]any{"description": desc, "agents": toAnySlice(targetAgents)}
	for k, v := range platformConfigs {
		metadata[k] = v
	}
	content := workspace.RenderSubagentText(metadata, body, "")

	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		fmt.Fprintf(stderr, "[ERROR] Failed to write subagent: %v\n", err)
		return 1
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		fmt.Fprintf(stderr, "[ERROR] Failed to write subagent: %v\n", err)
		return 1
	}

	verb := "CREATE"
	if exists {
		verb = "UPDATE"
	}
	fmt.Fprintf(stdout, "[%s FILE] %s\n", verb, displayPathRelativeToHome(path, env.Home))
	action := "Added"
	if exists {
		action = "Updated"
	}
	fmt.Fprintf(stdout, "[SUCCESS] %s subagent '%s'.\n", action, nameClean)

	if syncFlag {
		fmt.Fprintln(stderr, "[ERROR] --sync is not yet implemented in this Go build (needs sync_subagent_configs); run 'aikito sync subagents' separately.")
		return 1
	}
	fmt.Fprintln(stdout, "Next step: aikito sync subagents")
	return 0
}

var defaultSubagentAgents = []string{"codex", "claude-code", "agy", "github-copilot"}
var defaultMCPAgents = []string{"codex", "claude-code", "opencode", "agy", "github-copilot"}

func toAnySlice(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// --- add mcp ---
//
// Scope note: ports add_mcp's core workspace-side write (add.py:1524-1869)
// for the stdio/remote shapes driven by CLI flags. NOT ported: --from
// import (_resolve_mcp_source: fetching/parsing an external MCP source,
// including URL-based discovery) and the --sync preflight+apply flow
// (needs the MCP planner/executor, built in internal/mcp but not yet wired
// to a sync_mcp_configs-equivalent orchestration function) — both produce a
// clear error rather than silently no-op. Credential sanitization of
// URLs/headers (_sanitize_mcp_url/_sanitize_mcp_headers, see sanitize.go)
// IS ported and wired in below: this was flagged as a real security-hygiene
// gap during review (--url is a direct, reachable CLI flag) and fixed
// rather than left as a TODO.
func cmdAddMCP(args []string, stdout, stderr io.Writer, env Environment) int {
	var name, transport, command, url, agentsArg string
	var force, syncFlag, fromGiven bool
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--from":
			fromGiven = true
			i++
		case a == "--transport":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "[ERROR] --transport requires a value")
				return 2
			}
			transport = args[i]
		case a == "--command":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "[ERROR] --command requires a value")
				return 2
			}
			command = args[i]
		case a == "--url":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "[ERROR] --url requires a value")
				return 2
			}
			url = args[i]
		case a == "--agents":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "[ERROR] --agents requires a value")
				return 2
			}
			agentsArg = args[i]
		case a == "--force":
			force = true
		case a == "--sync":
			syncFlag = true
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(stderr, "[ERROR] Unknown flag: %s\n", a)
			return 2
		default:
			positional = append(positional, a)
		}
	}
	if fromGiven {
		fmt.Fprintln(stderr, "[ERROR] --from (importing an MCP server from an external source) is not yet implemented in this Go build.")
		return 2
	}
	if len(positional) > 0 {
		name = positional[0]
	}

	aikitoDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if msg := checkWorkspaceInitialized(aikitoDir); msg != "" {
		fmt.Fprintf(stderr, "[ERROR] %s\n", msg)
		return 1
	}
	if force && url == "" && command == "" && transport == "" {
		fmt.Fprintln(stderr, "[ERROR] --force requires --from or server configuration arguments when updating an MCP server.")
		return 1
	}
	if strings.TrimSpace(name) == "" {
		fmt.Fprintln(stderr, "[ERROR] MCP server name is required. Please specify a name or provide a source via --from.")
		return 1
	}
	nameClean := strings.TrimSpace(name)
	if msg := workspace.ValidateResourceName(nameClean, "mcp"); msg != "" {
		fmt.Fprintf(stderr, "[ERROR] %s\n", msg)
		return 1
	}
	if command != "" && url != "" {
		fmt.Fprintln(stderr, "[ERROR] Cannot specify both --command and --url.")
		return 1
	}
	if transport == "stdio" && url != "" {
		fmt.Fprintln(stderr, "[ERROR] Cannot specify --url when --transport is 'stdio'.")
		return 1
	}
	if transport == "remote" && command != "" {
		fmt.Fprintln(stderr, "[ERROR] Cannot specify --command when --transport is 'remote'.")
		return 1
	}
	if transport == "remote" && url == "" {
		fmt.Fprintln(stderr, "[ERROR] --url is required when --transport is 'remote'.")
		return 1
	}

	mcpsDir := filepath.Join(aikitoDir, "mcps")
	mcpFile := filepath.Join(mcpsDir, nameClean+".toml")

	fileAlreadyExists := false
	var existingData map[string]any
	if fi, serr := os.Stat(mcpFile); serr == nil && fi.Mode().IsRegular() {
		fileAlreadyExists = true
		if !force {
			fmt.Fprintf(stderr, "[ERROR] MCP server config already exists at %s. Use --force to overwrite.\n", displayPathRelativeToHome(mcpFile, env.Home))
			return 1
		}
		if data, rerr := os.ReadFile(mcpFile); rerr == nil {
			if doc, derr := workspace.DecodeTOML(data); derr == nil {
				existingData = doc
			}
		}
	}
	if existingData == nil {
		existingData = map[string]any{}
	}

	isRemote := transport == "remote" || (transport == "" && url != "")

	var targetAgents []string
	if agentsArg != "" {
		for _, a := range strings.Split(agentsArg, ",") {
			if t := strings.TrimSpace(a); t != "" {
				targetAgents = append(targetAgents, t)
			}
		}
	}
	if len(targetAgents) == 0 {
		if raw, ok := existingData["agents"].([]any); ok && len(raw) > 0 {
			for _, v := range raw {
				targetAgents = append(targetAgents, fmt.Sprint(v))
			}
		} else {
			targetAgents = append([]string{}, defaultMCPAgents...)
		}
	}
	// Python's agents_json = json.dumps(target_agents, ensure_ascii=False) uses
	// the DEFAULT ", " item separator; Go's encoding/json.Marshal is always
	// compact (no separator option), so it must not be used here — reuse
	// workspace.CanonicalJSON, which already matches Python's default spacing.
	agentsJSON := workspace.CanonicalJSON(toAnySlice(targetAgents))

	var lines []string
	if isRemote {
		safeURL, urlWarnings := SanitizeMCPURL(url, nameClean)
		for _, w := range urlWarnings {
			fmt.Fprintf(stderr, "[WARN] %s\n", w)
		}
		lines = append(lines,
			`transport = "remote"`,
			fmt.Sprintf("url = %s", workspace.CanonicalJSON(safeURL)),
			fmt.Sprintf("agents = %s", agentsJSON),
		)
		if headersRaw, ok := existingData["headers"].(map[string]any); ok && len(headersRaw) > 0 {
			headersStr := make(map[string]string, len(headersRaw))
			for k, v := range headersRaw {
				headersStr[k] = fmt.Sprint(v)
			}
			sanitizedHeaders, headerWarnings := SanitizeMCPHeaders(headersStr, nameClean)
			for _, w := range headerWarnings {
				fmt.Fprintf(stderr, "[WARN] %s\n", w)
			}
			sanitizedAny := make(map[string]any, len(sanitizedHeaders))
			for k, v := range sanitizedHeaders {
				sanitizedAny[k] = v
			}
			lines = append(lines, "headers = "+sync.FormatTomlValue(sanitizedAny))
		}
	} else {
		cmdVal := command
		if cmdVal == "" {
			if c, ok := existingData["command"].(string); ok {
				cmdVal = c
			} else {
				cmdVal = "npx"
			}
		}
		lines = append(lines, fmt.Sprintf("command = %s", workspace.CanonicalJSON(cmdVal)))
		argsVal := []any{}
		if a, ok := existingData["args"].([]any); ok {
			argsVal = a
		}
		lines = append(lines, fmt.Sprintf("args = %s", workspace.CanonicalJSON(argsVal)))
		lines = append(lines, fmt.Sprintf("agents = %s", agentsJSON))
		if env2, ok := existingData["env"].(map[string]any); ok && len(env2) > 0 {
			lines = append(lines, "env = "+sync.FormatTomlValue(env2))
		}
	}
	if auth, ok := existingData["authentication"].(map[string]any); ok && len(auth) > 0 {
		lines = append(lines, "\n[authentication]")
		for _, k := range sortedKeysOf(auth) {
			lines = append(lines, fmt.Sprintf("%s = %s", sync.FormatTomlKey(k), sync.FormatTomlValue(auth[k])))
		}
	}
	if overrides, ok := existingData["overrides"].(map[string]any); ok && len(overrides) > 0 {
		for _, agentKey := range sortedKeysOf(overrides) {
			if ov, ok := overrides[agentKey].(map[string]any); ok {
				lines = append(lines, fmt.Sprintf("\n[overrides.%s]", sync.FormatTomlKey(agentKey)))
				for _, k := range sortedKeysOf(ov) {
					lines = append(lines, fmt.Sprintf("%s = %s", sync.FormatTomlKey(k), sync.FormatTomlValue(ov[k])))
				}
			}
		}
	}
	mcpContent := strings.Join(lines, "\n") + "\n"
	if _, verr := workspace.DecodeTOML([]byte(mcpContent)); verr != nil {
		fmt.Fprintf(stderr, "[ERROR] Failed to generate MCP configuration: %v\n", verr)
		return 1
	}
	if err := os.MkdirAll(mcpsDir, 0o777); err != nil {
		fmt.Fprintf(stderr, "[ERROR] Failed to create MCP config directory: %v\n", err)
		return 1
	}
	if err := os.WriteFile(mcpFile, []byte(mcpContent), 0o644); err != nil {
		fmt.Fprintf(stderr, "[ERROR] Failed to write MCP config: %v\n", err)
		return 1
	}

	if fileAlreadyExists {
		fmt.Fprintf(stdout, "[UPDATE FILE] %s\n", displayPathRelativeToHome(mcpFile, env.Home))
		fmt.Fprintf(stdout, "\n[SUCCESS] Updated MCP server '%s'.\n", nameClean)
	} else {
		fmt.Fprintf(stdout, "[CREATE FILE] %s\n", displayPathRelativeToHome(mcpFile, env.Home))
		fmt.Fprintf(stdout, "\n[SUCCESS] Added MCP server '%s'.\n", nameClean)
	}

	if syncFlag {
		fmt.Fprintln(stderr, "[ERROR] --sync is not yet implemented in this Go build (needs an mcp.sync_mcp_configs equivalent orchestrating internal/mcp's adapters); run 'aikito sync mcp' separately.")
		return 1
	}
	fmt.Fprintln(stdout, "\U0001F4A1 Next steps:")
	fmt.Fprintf(stdout, "  1. Configure server in %s (or run 'aikito edit mcp %s')\n", displayPathRelativeToHome(mcpFile, env.Home), nameClean)
	fmt.Fprintln(stdout, "  2. Synchronize to agents: aikito sync mcp")
	return 0
}

func sortedKeysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}
