package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-go/internal/compat"
	"github.com/mr-miles/aikito-go/internal/mcp"
	"github.com/mr-miles/aikito-go/internal/sync"
	"github.com/mr-miles/aikito-go/internal/workspace"
)

// genericMCPPathNames is add.py's GENERIC_MCP_PATH_NAMES: URL path
// segments too generic to name a server after.
var genericMCPPathNames = map[string]bool{
	"mcp": true, "v1": true, "v2": true, "sse": true, "api": true,
	"tools": true, "tool": true, "endpoint": true, "server": true,
}

// importedMCP is add.py's ImportedMCP. headerKeys keeps insertion order.
type importedMCP struct {
	name       *string
	url        string
	headerKeys []string
	headers    map[string]string
	agents     []string
}

// orderedHeaders is a header dict that keeps Python's insertion order.
type orderedHeaders struct {
	keys []string
	vals map[string]string
}

func (h *orderedHeaders) set(k, v string) {
	if h.vals == nil {
		h.vals = map[string]string{}
	}
	if _, ok := h.vals[k]; !ok {
		h.keys = append(h.keys, k)
	}
	h.vals[k] = v
}

// sanitizeHeadersOrdered is add.py's _sanitize_mcp_headers, keeping the
// warnings in header order.
func sanitizeHeadersOrdered(h orderedHeaders, serverName string) (orderedHeaders, []string) {
	var out orderedHeaders
	var warnings []string
	for _, k := range h.keys {
		single, w := SanitizeMCPHeaders(map[string]string{k: h.vals[k]}, serverName)
		out.set(k, single[k])
		warnings = append(warnings, w...)
	}
	return out, warnings
}

// plainTOMLValue turns decoded values whose tables are OrderedObjects into
// the plain shapes sync.FormatTomlValue and pyRepr take.
func plainTOMLValue(v any) any {
	switch x := v.(type) {
	case *mcp.OrderedObject:
		m := make(map[string]any, x.Len())
		for _, k := range x.Keys() {
			val, _ := x.Get(k)
			m[k] = plainTOMLValue(val)
		}
		return m
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = plainTOMLValue(item)
		}
		return out
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	}
	return v
}

// pyStrOrdered is str() of a decoded value, where tables are OrderedObjects.
func pyStrOrdered(v any) string {
	if o, ok := v.(*mcp.OrderedObject); ok {
		return pyRepr(plainTOMLValue(o))
	}
	if list, ok := v.([]any); ok {
		return pyRepr(plainTOMLValue(list))
	}
	return pyStr(v)
}

func asOrdered(v any) (*mcp.OrderedObject, bool) {
	o, ok := v.(*mcp.OrderedObject)
	return o, ok
}

// selectMCPServer is add.py's _select_mcp_server.
func selectMCPServer(servers *mcp.OrderedObject, name *string, sourceDesc string) (string, any, error) {
	if name != nil && strings.TrimSpace(*name) != "" {
		target := strings.TrimSpace(*name)
		for _, k := range servers.Keys() {
			if k == target || strings.ReplaceAll(k, "_", "-") == target || strings.ReplaceAll(k, "-", "_") == target {
				v, _ := servers.Get(k)
				return target, v, nil
			}
		}
		return "", nil, fmt.Errorf("Server '%s' not found in '%s'.", target, sourceDesc)
	}
	keys := servers.Keys()
	switch {
	case len(keys) == 1:
		v, _ := servers.Get(keys[0])
		return strings.ToLower(strings.ReplaceAll(keys[0], "_", "-")), v, nil
	case len(keys) > 1:
		sorted := append([]string{}, keys...)
		sort.Strings(sorted)
		return "", nil, fmt.Errorf("Source file contains multiple MCP servers (%s). Please specify which server to import via 'name'.", strings.Join(sorted, ", "))
	}
	return "", nil, fmt.Errorf("No MCP servers found in '%s'.", sourceDesc)
}

// firstTruthy is Python's `a or b or c` over document keys.
func firstTruthy(doc *mcp.OrderedObject, keys ...string) any {
	var last any
	for _, k := range keys {
		v, _ := doc.Get(k)
		last = v
		if pyTruthyOrdered(v) {
			return v
		}
	}
	return last
}

func pyTruthyOrdered(v any) bool {
	if o, ok := v.(*mcp.OrderedObject); ok {
		return o.Len() > 0
	}
	return pyTruthy(plainTOMLValue(v))
}

// resolveMCPSource is add.py's _resolve_mcp_source. fromPath is from made
// absolute against the command's cwd (used only for local files).
func resolveMCPSource(from, fromPath string, name *string, home string, stderr io.Writer) (importedMCP, error) {
	if strings.HasPrefix(from, "http://") || strings.HasPrefix(from, "https://") {
		parsed, err := url.Parse(from)
		if err != nil || parsed.Host == "" && parsed.User == nil {
			return importedMCP{}, fmt.Errorf("Invalid remote MCP URL: %s", from)
		}
		var inferred *string
		if name != nil && strings.TrimSpace(*name) != "" {
			n := strings.TrimSpace(*name)
			inferred = &n
		} else {
			var parts []string
			for _, p := range strings.Split(parsed.EscapedPath(), "/") {
				if p != "" {
					parts = append(parts, p)
				}
			}
			if len(parts) > 0 {
				cand := strings.ToLower(strings.ReplaceAll(parts[len(parts)-1], "_", "-"))
				if !genericMCPPathNames[cand] && workspace.ValidateResourceName(cand, "mcp") == "" {
					inferred = &cand
				}
			}
		}
		temp := "mcp"
		if inferred != nil {
			temp = *inferred
		} else if name != nil && *name != "" {
			temp = *name
		}
		safe, warnings := SanitizeMCPURL(from, temp)
		for _, w := range warnings {
			fmt.Fprintf(stderr, "[WARN] %s\n", w)
		}
		return importedMCP{name: inferred, url: safe}, nil
	}

	sourcePath, _ := compat.ResolvePath(fromPath)
	info, err := os.Stat(sourcePath)
	if err != nil {
		return importedMCP{}, fmt.Errorf("Source file does not exist: %s", from)
	}
	if !info.Mode().IsRegular() {
		return importedMCP{}, fmt.Errorf("Source path must be a JSON or TOML file: %s", displayPathRelativeToHome(sourcePath, home))
	}
	ext := strings.ToLower(filepath.Ext(sourcePath))
	if ext != ".json" && ext != ".jsonc" && ext != ".toml" {
		return importedMCP{}, fmt.Errorf("Unsupported file format '%s'. Expected .json, .jsonc, or .toml", filepath.Base(sourcePath))
	}
	sourceDesc := displayPathRelativeToHome(sourcePath, home)
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		return importedMCP{}, err
	}
	content := string(raw)
	stem := strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath))
	defaultName := func() string {
		if name != nil && strings.TrimSpace(*name) != "" {
			return strings.TrimSpace(*name)
		}
		return stem
	}

	var serverCfg any
	var inferredName *string
	pick := func(doc *mcp.OrderedObject, keys ...string) error {
		if servers, ok := asOrdered(firstTruthy(doc, keys...)); ok {
			n, cfg, err := selectMCPServer(servers, name, sourceDesc)
			if err != nil {
				return err
			}
			inferredName, serverCfg = &n, cfg
			return nil
		}
		n := defaultName()
		inferredName, serverCfg = &n, doc
		return nil
	}
	if ext == ".toml" {
		doc, err := mcp.DecodeTOMLOrdered(content)
		if err != nil {
			return importedMCP{}, fmt.Errorf("Failed to parse TOML from '%s': %v", sourceDesc, err)
		}
		if err := pick(doc, "servers", "mcp_servers"); err != nil {
			return importedMCP{}, err
		}
	} else {
		doc, err := mcp.ParseJSONOrdered(content)
		if err != nil {
			doc, err = mcp.ParseJSONC(content)
			if err != nil {
				return importedMCP{}, fmt.Errorf("Failed to parse JSON from '%s': %v", sourceDesc, err)
			}
		}
		obj, ok := doc.(*mcp.OrderedObject)
		if !ok {
			return importedMCP{}, fmt.Errorf("JSON content in '%s' must be an object.", sourceDesc)
		}
		if err := pick(obj, "mcpServers", "mcp_servers", "servers"); err != nil {
			return importedMCP{}, err
		}
	}

	cfg, ok := asOrdered(serverCfg)
	if !ok {
		return importedMCP{}, fmt.Errorf("MCP server configuration in '%s' must be a dictionary/table.", sourceDesc)
	}
	urlVal := firstTruthy(cfg, "url", "serverUrl")
	command, _ := cfg.Get("command")
	if !pyTruthyOrdered(urlVal) {
		if pyTruthyOrdered(command) {
			who := "None"
			if inferredName != nil && *inferredName != "" {
				who = *inferredName
			} else if name != nil {
				who = *name
			}
			return importedMCP{}, fmt.Errorf("Aikito currently supports synchronizing remote MCP servers. Stdio server '%s' without a URL cannot be imported as a remote MCP.", who)
		}
		return importedMCP{}, fmt.Errorf("No remote URL found for MCP server in '%s'.", sourceDesc)
	}
	urlStr, isStr := urlVal.(string)
	if !isStr || !(strings.HasPrefix(urlStr, "http://") || strings.HasPrefix(urlStr, "https://")) {
		return importedMCP{}, fmt.Errorf("Invalid remote MCP URL '%s': must begin with http:// or https://", pyStrOrdered(urlVal))
	}

	var headers orderedHeaders
	if h, ok := asOrdered(cfg.GetOr("env_http_headers", nil)); ok {
		for _, k := range h.Keys() {
			v, _ := h.Get(k)
			s := pyStrOrdered(v)
			if mcp.EnvironmentReference(s) == "" && workspace.PyStrip(s) != "" {
				s = "${" + workspace.PyStrip(s) + "}"
			}
			headers.set(k, s)
		}
	}
	for _, key := range []string{"http_headers", "headers"} {
		if h, ok := asOrdered(cfg.GetOr(key, nil)); ok {
			for _, k := range h.Keys() {
				v, _ := h.Get(k)
				headers.set(k, pyStrOrdered(v))
			}
		}
	}

	var agents []string
	if list, ok := cfg.GetOr("agents", nil).([]any); ok && len(list) > 0 {
		for _, a := range list {
			if s := workspace.PyStrip(pyStrOrdered(a)); s != "" {
				agents = append(agents, s)
			}
		}
	}
	return importedMCP{name: inferredName, url: urlStr, headerKeys: headers.keys, headers: headers.vals, agents: agents}, nil
}

// cmdAddMCP ports cli.py cmd_add_mcp and add.py add_mcp.
func cmdAddMCP(args []string, stdout, stderr io.Writer, env Environment) int {
	parsed, ok := parseArgparseOpts("add mcp", args,
		[]string{"--sync", "--force"},
		[]string{"--from", "--transport", "--command", "--url", "--agents"}, nil, 1, stderr)
	if !ok {
		return 2
	}
	if t, ok := parsed.values["--transport"]; ok && t != "stdio" && t != "remote" {
		fmt.Fprintf(stderr, "%saikito add mcp: error: argument --transport: invalid choice: '%s' (choose from stdio, remote)\n", subcommandUsage("add mcp"), t)
		return 2
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
	var agents []string
	if a := parsed.values["--agents"]; a != "" {
		for _, s := range strings.Split(a, ",") {
			if s = strings.TrimSpace(s); s != "" {
				agents = append(agents, s)
			}
		}
	}
	o := addMCPOptions{
		transport: parsed.values["--transport"], command: parsed.values["--command"],
		url: parsed.values["--url"], agents: agents,
		sync: parsed.flags["--sync"], force: parsed.flags["--force"],
	}
	if len(parsed.positionals) > 0 {
		o.name = &parsed.positionals[0]
	}
	if f, ok := parsed.values["--from"]; ok {
		o.from = &f
		o.fromPath = resolveAgainstCwd(env, f)
	}
	if !addMCP(aikitoDir, env.Home, o, stdout, stderr) {
		return 1
	}
	return 0
}

type addMCPOptions struct {
	name, from              *string
	fromPath                string
	transport, command, url string
	agents                  []string
	sync, force             bool
}

func addMCP(aikitoDir, home string, o addMCPOptions, stdout, stderr io.Writer) bool {
	if r, err := compat.ResolvePath(aikitoDir); err == nil {
		aikitoDir = r
	}
	if r, err := compat.ResolvePath(home); err == nil {
		home = r
	}
	errf := func(format string, a ...any) bool {
		fmt.Fprintf(stderr, "[ERROR] "+format+"\n", a...)
		return false
	}
	disp := func(p string) string { return displayPathRelativeToHome(p, home) }

	if msg := checkWorkspaceInitialized(aikitoDir); msg != "" {
		return errf("%s", msg)
	}
	if o.force && o.from == nil && o.url == "" && o.command == "" && o.transport == "" {
		return errf("--force requires --from or server configuration arguments when updating an MCP server.")
	}

	name := strOrEmpty(o.name)
	transport, command, urlArg, agents := o.transport, o.command, o.url, o.agents
	var headers *orderedHeaders
	var imported *importedMCP
	if o.from != nil {
		im, err := resolveMCPSource(*o.from, o.fromPath, o.name, home, stderr)
		if err != nil {
			return errf("%v", err)
		}
		imported = &im
		if name == "" && im.name != nil && *im.name != "" {
			name = *im.name
		}
		if urlArg == "" && im.url != "" {
			urlArg = im.url
		}
		if transport == "" {
			transport = "remote"
		}
		if len(im.headerKeys) > 0 {
			headers = &orderedHeaders{keys: im.headerKeys, vals: im.headers}
		}
		if agents == nil && len(im.agents) > 0 {
			agents = im.agents
		}
	}

	if strings.TrimSpace(name) == "" {
		return errf("MCP server name is required. Please specify a name or provide a source via --from.")
	}
	nameClean := strings.TrimSpace(name)
	if msg := workspace.ValidateResourceName(nameClean, "mcp"); msg != "" {
		return errf("%s", msg)
	}
	switch {
	case command != "" && urlArg != "":
		return errf("Cannot specify both --command and --url.")
	case transport == "stdio" && urlArg != "":
		return errf("Cannot specify --url when --transport is 'stdio'.")
	case transport == "remote" && command != "":
		return errf("Cannot specify --command when --transport is 'remote'.")
	case transport == "remote" && urlArg == "":
		return errf("--url is required when --transport is 'remote'.")
	}

	mcpsDir := filepath.Join(aikitoDir, "mcps")
	mcpFile := filepath.Join(mcpsDir, nameClean+".toml")
	_, statErr := os.Stat(mcpFile)
	fileAlreadyExists := statErr == nil
	if fileAlreadyExists && !o.force {
		return errf("MCP server config already exists at %s. Use --force to overwrite.", disp(mcpFile))
	}
	existing := mcp.NewOrderedObject()
	if fileAlreadyExists {
		if raw, err := os.ReadFile(mcpFile); err == nil {
			if doc, derr := mcp.DecodeTOMLOrdered(string(raw)); derr == nil {
				existing = doc
			}
		}
	}

	isRemote := transport == "remote" || (transport == "" && urlArg != "")

	var targetAgents []string
	switch {
	case len(agents) > 0:
		targetAgents = agents
	case imported != nil && len(imported.agents) > 0:
		targetAgents = imported.agents
	default:
		if list, ok := existing.GetOr("agents", nil).([]any); fileAlreadyExists && ok && len(list) > 0 {
			for _, v := range list {
				targetAgents = append(targetAgents, pyStrOrdered(v))
			}
		} else {
			targetAgents = append([]string{}, defaultMCPAgents...)
		}
	}

	if headers == nil {
		if h, ok := asOrdered(existing.GetOr("headers", nil)); ok {
			headers = &orderedHeaders{}
			for _, k := range h.Keys() {
				v, _ := h.Get(k)
				headers.set(k, pyStrOrdered(v))
			}
		}
	}
	if headers != nil && len(headers.keys) > 0 {
		clean, warnings := sanitizeHeadersOrdered(*headers, nameClean)
		for _, w := range warnings {
			fmt.Fprintf(stderr, "[WARN] %s\n", w)
		}
		headers = &clean
	}

	agentsJSON := workspace.CanonicalJSON(toAnySlice(targetAgents))
	var lines []string
	sortedTable := func(keys []string, val func(string) string) string {
		keys = append([]string{}, keys...)
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = sync.FormatTomlKey(k) + " = " + val(k)
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	}
	if isRemote {
		safe, warnings := SanitizeMCPURL(urlArg, nameClean)
		for _, w := range warnings {
			fmt.Fprintf(stderr, "[WARN] %s\n", w)
		}
		lines = append(lines, `transport = "remote"`, "url = "+workspace.CanonicalJSON(safe), "agents = "+agentsJSON)
		if headers != nil && len(headers.keys) > 0 {
			lines = append(lines, "headers = "+sortedTable(headers.keys, func(k string) string {
				return sync.FormatTomlValue(headers.vals[k])
			}))
		}
	} else {
		cmdVal := any(command)
		if command == "" {
			cmdVal = plainTOMLValue(existing.GetOr("command", "npx"))
		}
		lines = append(lines, "command = "+workspace.CanonicalJSON(cmdVal))
		argsVal := any([]any{})
		if list, ok := existing.GetOr("args", nil).([]any); ok {
			argsVal = plainTOMLValue(list)
		}
		lines = append(lines, "args = "+workspace.CanonicalJSON(argsVal), "agents = "+agentsJSON)
		if envTable, ok := asOrdered(existing.GetOr("env", nil)); ok {
			lines = append(lines, "env = "+sortedTable(envTable.Keys(), func(k string) string {
				v, _ := envTable.Get(k)
				return sync.FormatTomlValue(pyStrOrdered(v))
			}))
		}
	}
	if auth, ok := asOrdered(existing.GetOr("authentication", nil)); ok {
		lines = append(lines, "\n[authentication]")
		keys := append([]string{}, auth.Keys()...)
		sort.Strings(keys)
		for _, k := range keys {
			v, _ := auth.Get(k)
			lines = append(lines, sync.FormatTomlKey(k)+" = "+sync.FormatTomlValue(plainTOMLValue(v)))
		}
	}
	if overrides, ok := asOrdered(existing.GetOr("overrides", nil)); ok {
		agentKeys := append([]string{}, overrides.Keys()...)
		sort.Strings(agentKeys)
		for _, ak := range agentKeys {
			ov, ok := asOrdered(overrides.GetOr(ak, nil))
			if !ok {
				continue
			}
			lines = append(lines, "\n[overrides."+sync.FormatTomlKey(ak)+"]")
			keys := append([]string{}, ov.Keys()...)
			sort.Strings(keys)
			for _, k := range keys {
				v, _ := ov.Get(k)
				lines = append(lines, sync.FormatTomlKey(k)+" = "+sync.FormatTomlValue(plainTOMLValue(v)))
			}
		}
	}
	content := strings.Join(lines, "\n") + "\n"
	if _, err := workspace.DecodeTOML([]byte(content)); err != nil {
		return errf("Failed to generate MCP configuration: %v", err)
	}
	if err := os.MkdirAll(mcpsDir, 0o777); err != nil {
		return errf("Failed to create MCP config directory: %v", err)
	}

	backupDir, stagedBackup := "", ""
	if fileAlreadyExists {
		d, err := os.MkdirTemp(mcpsDir, "."+nameClean+".add_backup.")
		if err == nil {
			backupDir = d
			stagedBackup = filepath.Join(d, filepath.Base(mcpFile))
			err = copyFile2(mcpFile, stagedBackup)
		}
		if err != nil {
			if backupDir != "" {
				os.RemoveAll(backupDir)
			}
			return errf("Failed to backup existing MCP config file: %v", err)
		}
	}
	restore := func() {
		if stagedBackup != "" {
			if _, err := os.Stat(stagedBackup); err == nil {
				_ = os.Rename(stagedBackup, mcpFile)
			}
		} else if !fileAlreadyExists {
			_ = os.Remove(mcpFile)
		}
		if backupDir != "" {
			os.RemoveAll(backupDir)
		}
	}
	if err := atomicWriteText(mcpFile, content); err != nil {
		restore()
		return errf("Failed to write MCP config: %v", err)
	}

	if o.sync {
		fmt.Fprintf(stdout, "\n[SYNC] Synchronizing MCP server '%s' to target agent platforms...\n", nameClean)
		reverted := "Reverted changes to " + disp(mcpFile) + "."
		plan, err := mcp.BuildMCPPlan(aikitoDir, home, mcp.BuildMCPPlanOptions{})
		if err != nil {
			restore()
			fmt.Fprintf(stderr, "[ERROR] %v\n", err)
			return errf("Synchronization failed. %s", reverted)
		}
		var preflight bytes.Buffer
		dryOK, err := syncMCPConfigs(plan, home, true, func(line string) { preflight.WriteString(line + "\n") })
		if err == nil && !dryOK {
			restore()
			if msg := workspace.PyStrip(preflight.String()); msg != "" {
				fmt.Fprintln(stderr, msg)
			}
			return errf("Synchronization preflight failed due to conflict. %s", reverted)
		}
		ok := false
		if err == nil {
			ok, err = syncMCPConfigs(plan, home, false, func(line string) { fmt.Fprintln(stdout, line) })
		}
		if err != nil {
			restore()
			fmt.Fprintf(stderr, "[ERROR] %v\n", err)
			return errf("Synchronization failed. %s", reverted)
		}
		if !ok {
			restore()
			return errf("Synchronization failed. %s", reverted)
		}
	}
	if backupDir != "" {
		os.RemoveAll(backupDir)
	}

	if fileAlreadyExists {
		fmt.Fprintf(stdout, "[UPDATE FILE] %s\n\n[SUCCESS] Updated MCP server '%s'.\n", disp(mcpFile), nameClean)
	} else {
		fmt.Fprintf(stdout, "[CREATE FILE] %s\n\n[SUCCESS] Added MCP server '%s'.\n", disp(mcpFile), nameClean)
	}
	if !o.sync {
		fmt.Fprintln(stdout, "\U0001F4A1 Next steps:")
		fmt.Fprintf(stdout, "  1. Configure server in %s (or run 'aikito edit mcp %s')\n", disp(mcpFile), nameClean)
		fmt.Fprintln(stdout, "  2. Synchronize to agents: aikito sync mcp")
	}
	return true
}

// syncMCPConfigs ports mcp/__init__.py sync_mcp_configs for a prebuilt plan:
// inspection lines, then either the dry-run listing or the executor. The
// error is the MCPConfigError Python would raise.
func syncMCPConfigs(plan mcp.MCPPlan, home string, dryRun bool, output func(string)) (bool, error) {
	for _, op := range plan.Operations {
		targetKey := op.Target.Agent + "/" + op.Target.LogicalIdentity
		switch op.Action {
		case "SKIP":
			switch {
			case op.Spec != nil && op.Spec.MissingCredentialEnv != "":
				output(fmt.Sprintf("[WARN] %s: skipped due to missing credential environment variable: %s", targetKey, op.Spec.MissingCredentialEnv))
			case op.Spec != nil && !op.Spec.Enabled:
				output(fmt.Sprintf("[SKIP] %s: %s", targetKey, op.Reason))
			default:
				output(fmt.Sprintf("[SKIP] %s not detected: %s", op.Target.Agent, parentDir(op.Target.Path)))
			}
		case "NOOP":
			output(fmt.Sprintf("[OK] %s: already synchronized", targetKey))
		case "CONFLICT":
			output(fmt.Sprintf("[CONFLICT] %s: existing config was not last written by aikito; review it or rerun with --force", targetKey))
		}
	}
	if dryRun {
		for _, op := range plan.Operations {
			if (op.Action == "CREATE" || op.Action == "UPDATE") && op.IsAuthorized {
				action := "create"
				if op.Action == "UPDATE" {
					action = "update"
				}
				output(fmt.Sprintf("[DRY-RUN] %s/%s: would %s entry", op.Target.Agent, op.Target.LogicalIdentity, action))
			}
		}
		return plan.CanApply(), nil
	}
	if !plan.CanApply() {
		return false, nil
	}
	result, err := mcp.ExecuteMCPPlan(plan, home, output)
	if err != nil {
		return false, err
	}
	return result.Success, nil
}
