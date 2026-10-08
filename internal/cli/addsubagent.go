package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-go/internal/compat"
	"github.com/mr-miles/aikito-go/internal/registry"
	"github.com/mr-miles/aikito-go/internal/subagent"
	"github.com/mr-miles/aikito-go/internal/workspace"
	"github.com/mr-miles/aikito-go/internal/writerlock"
)

// importedSubagent is add.py's ImportedSubagent.
type importedSubagent struct {
	name, description *string
	instructions      string
	targetAgents      []string
	explicitPlatforms map[string]map[string]any
	topLevelOptions   map[string]any
}

// validatePlatformOpts ports subagent_validation.py validate_platform_opts
// against the workspace's agent definitions.
func validatePlatformOpts(agentName, subagentName string, opts any, defs map[string]registry.AgentDefinition) (map[string]any, error) {
	def, ok := defs[agentName]
	if !ok || def.Subagents == nil {
		return nil, fmt.Errorf("Subagent '%s' platform '%s' has no defined subagents capability", subagentName, agentName)
	}
	adapter, err := subagent.GetSubagentAdapter(def.Subagents.ConfigFormat)
	if err != nil {
		return nil, err
	}
	m, _ := opts.(map[string]any)
	return adapter.ValidateOptions(agentName, subagentName, m)
}

// resolveSubagentSource ports add.py's _resolve_subagent_source. fromPath
// is from made absolute against the command's cwd.
func resolveSubagentSource(from, fromPath string, name, description *string, home, aikitoDir string) (importedSubagent, error) {
	disp := func(p string) string { return displayPathRelativeToHome(p, home) }
	sourcePath, _ := compat.ResolvePath(fromPath)
	info, err := os.Stat(sourcePath)
	if err != nil {
		return importedSubagent{}, fmt.Errorf("Source path does not exist: %s", from)
	}
	var sourceMD string
	switch {
	case info.Mode().IsRegular():
		if !strings.HasSuffix(filepath.Base(sourcePath), ".md") {
			return importedSubagent{}, fmt.Errorf("Source file '%s' must be a markdown (.md) file.", disp(sourcePath))
		}
		sourceMD = sourcePath
	case info.IsDir():
		var candidates []string
		if name != nil && strings.TrimSpace(*name) != "" {
			candidates = append(candidates, filepath.Join(sourcePath, strings.TrimSpace(*name)+".md"))
		}
		for _, c := range []string{"instructions.md", "prompt.md", "subagent.md", filepath.Base(sourcePath) + ".md"} {
			candidates = append(candidates, filepath.Join(sourcePath, c))
		}
		for _, c := range candidates {
			if isRegularFile(c) {
				sourceMD = c
				break
			}
		}
		if sourceMD == "" {
			entries, _ := os.ReadDir(sourcePath)
			var mds []string
			for _, e := range entries {
				p := filepath.Join(sourcePath, e.Name())
				if strings.HasSuffix(e.Name(), ".md") && isRegularFile(p) {
					mds = append(mds, p)
				}
			}
			sort.Strings(mds)
			switch {
			case len(mds) == 1:
				sourceMD = mds[0]
			case len(mds) > 1:
				return importedSubagent{}, fmt.Errorf("Multiple markdown files found in '%s'. Please specify the file directly with --from.", disp(sourcePath))
			default:
				return importedSubagent{}, fmt.Errorf("Source directory '%s' does not contain a subagent markdown file.", disp(sourcePath))
			}
		}
	default:
		return importedSubagent{}, fmt.Errorf("Source path is not a file or directory: %s", from)
	}

	defs, err := registry.LoadAgentDefinitions(aikitoDir, home)
	if err != nil {
		return importedSubagent{}, err
	}
	adapters := map[string]subagent.Adapter{}
	var platformNames []string
	for n, d := range defs {
		if d.Subagents == nil {
			continue
		}
		a, aerr := subagent.GetSubagentAdapter(d.Subagents.ConfigFormat)
		if aerr != nil {
			return importedSubagent{}, aerr
		}
		adapters[n] = a
		platformNames = append(platformNames, n)
	}
	raw, err := os.ReadFile(sourceMD)
	if err != nil {
		return importedSubagent{}, fmt.Errorf("Failed to read source file '%s': %v", disp(sourceMD), err)
	}
	meta, body := workspace.ParseMarkdownFrontmatter(string(raw), platformNames)

	instructions := workspace.PyStrip(body)
	if instructions == "" {
		return importedSubagent{}, fmt.Errorf("Source file '%s' does not contain any instructions.", disp(sourceMD))
	}
	instructions += "\n"

	var inferredName string
	switch {
	case name != nil && strings.TrimSpace(*name) != "":
		inferredName = strings.TrimSpace(*name)
	case pyTruthy(meta["name"]):
		inferredName = workspace.PyStrip(pyStr(meta["name"]))
	case info.IsDir():
		inferredName = filepath.Base(sourcePath)
	case strings.HasSuffix(filepath.Base(sourceMD), ".agent.md"):
		inferredName = strings.TrimSuffix(filepath.Base(sourceMD), ".agent.md")
	default:
		stem := strings.TrimSuffix(filepath.Base(sourceMD), filepath.Ext(sourceMD))
		switch strings.ToLower(stem) {
		case "instructions", "prompt", "subagent", "agent":
			inferredName = filepath.Base(filepath.Dir(sourceMD))
		default:
			inferredName = stem
		}
	}
	out := importedSubagent{
		name: &inferredName, instructions: instructions,
		explicitPlatforms: map[string]map[string]any{}, topLevelOptions: map[string]any{},
	}
	switch {
	case description != nil && strings.TrimSpace(*description) != "":
		d := strings.TrimSpace(*description)
		out.description = &d
	case pyTruthy(meta["description"]):
		d := workspace.PyStrip(pyStr(meta["description"]))
		out.description = &d
	}
	if list, ok := meta["agents"].([]any); ok && len(list) > 0 {
		for _, a := range list {
			if s := workspace.PyStrip(pyStr(a)); s != "" {
				out.targetAgents = append(out.targetAgents, s)
			}
		}
	}

	adapterFields := map[string]bool{}
	for _, a := range adapters {
		for f := range a.AllowedFields {
			adapterFields[f] = true
		}
	}
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	nameForErrors := inferredName
	if nameForErrors == "" {
		nameForErrors = "subagent"
	}
	for _, plat := range keys {
		value, isTable := meta[plat].(map[string]any)
		if !isTable || adapterFields[plat] {
			continue
		}
		switch plat {
		case "name", "description", "agents", "metadata":
			continue
		}
		validated, verr := validatePlatformOpts(plat, nameForErrors, value, defs)
		if verr != nil {
			return importedSubagent{}, verr
		}
		out.explicitPlatforms[plat] = validated
	}
	for _, k := range keys {
		switch k {
		case "name", "description", "agents":
			continue
		}
		if _, isPlatform := adapters[k]; isPlatform {
			continue
		}
		if adapterFields[k] {
			out.topLevelOptions[k] = meta[k]
		}
	}
	return out, nil
}

// cmdAddSubagent ports cli.py cmd_add_subagent and add.py add_subagent.
func cmdAddSubagent(args []string, stdout, stderr io.Writer, env Environment) int {
	parsed, ok := parseArgparseOpts("add subagent", args,
		[]string{"--sync", "--force"},
		[]string{"--from", "--description", "--agents"}, nil, 1, stderr)
	if !ok {
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
	if r, err := compat.ResolvePath(aikitoDir); err == nil {
		aikitoDir = r
	}
	home := env.Home
	if r, err := compat.ResolvePath(home); err == nil {
		home = r
	}
	errf := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "[ERROR] "+format+"\n", a...)
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
	var name, description *string
	if len(parsed.positionals) > 0 {
		name = &parsed.positionals[0]
	}
	if d, ok := parsed.values["--description"]; ok {
		description = &d
	}
	from, hasFrom := parsed.values["--from"]
	force := parsed.flags["--force"]

	if err := workspace.RequireCurrentLayout(aikitoDir); err != nil {
		return errf("%v", err)
	}
	if force && !hasFrom {
		return errf("--force requires --from when adding a subagent.")
	}
	var imported *importedSubagent
	if hasFrom {
		im, ierr := resolveSubagentSource(from, resolveAgainstCwd(env, from), name, description, home, aikitoDir)
		if ierr != nil {
			return errf("%v", ierr)
		}
		imported = &im
	}
	nameClean := workspace.PyStrip(strOrEmpty(name))
	if imported != nil {
		nameClean = workspace.PyStrip(*imported.name)
	}
	if msg := workspace.ValidateResourceName(nameClean, "subagent"); msg != "" {
		return errf("%s", msg)
	}

	path := filepath.Join(aikitoDir, "subagents", nameClean+".md")
	exists := false
	if _, serr := os.Stat(path); serr == nil {
		exists = true
		if li, _ := os.Lstat(path); li.Mode()&os.ModeSymlink != 0 || !li.Mode().IsRegular() {
			return errf("Unsafe subagent file: %s", path)
		}
		if !force {
			return errf("Subagent '%s' is already registered.", nameClean)
		}
	}
	oldMetadata := map[string]any{}
	if exists {
		meta, _, perr := workspace.ParseSubagentFile(path)
		if perr != nil {
			return errf("%v", perr)
		}
		oldMetadata = meta
	}

	var targetAgents any
	switch {
	case len(agents) > 0:
		targetAgents = toAnySlice(agents)
	case imported != nil && len(imported.targetAgents) > 0:
		targetAgents = toAnySlice(imported.targetAgents)
	case pyTruthy(oldMetadata["agents"]):
		targetAgents = oldMetadata["agents"]
	default:
		targetAgents = toAnySlice(defaultSubagentAgents)
	}

	desc := any(workspace.PyStrip(strOrEmpty(description)))
	switch {
	case desc != "":
	case imported != nil && imported.description != nil && *imported.description != "":
		desc = *imported.description
	case pyTruthy(oldMetadata["description"]):
		desc = oldMetadata["description"]
	default:
		desc = fmt.Sprintf("Subagent %s.", nameClean)
	}

	platformConfigs := map[string]map[string]any{}
	for k, v := range oldMetadata {
		if k == "description" || k == "agents" {
			continue
		}
		if m, ok := v.(map[string]any); ok {
			platformConfigs[k] = copyMap(m)
		}
	}
	if imported != nil {
		for plat, opts := range imported.explicitPlatforms {
			if platformConfigs[plat] == nil {
				platformConfigs[plat] = map[string]any{}
			}
			for k, v := range opts {
				platformConfigs[plat][k] = v
			}
		}
		if len(imported.topLevelOptions) > 0 {
			list, _ := targetAgents.([]any)
			if len(list) != 1 {
				return errf("Top-level platform options require one target Agent.")
			}
			target := pyStr(list[0])
			if platformConfigs[target] == nil {
				platformConfigs[target] = map[string]any{}
			}
			for k, v := range imported.topLevelOptions {
				platformConfigs[target][k] = v
			}
		}
	}

	defs, derr := registry.LoadAgentDefinitions(aikitoDir, home)
	if derr != nil {
		return errf("%v", derr)
	}
	plats := make([]string, 0, len(platformConfigs))
	for p := range platformConfigs {
		plats = append(plats, p)
	}
	sort.Strings(plats)
	for _, p := range plats {
		if _, verr := validatePlatformOpts(p, nameClean, platformConfigs[p], defs); verr != nil {
			return errf("%v", verr)
		}
	}
	body := fmt.Sprintf("# %s\n\nAdd developer instructions for the %s subagent here.\n", titleize(nameClean), nameClean)
	if imported != nil {
		body = imported.instructions
	}
	metadata := map[string]any{"description": desc, "agents": targetAgents}
	for p, opts := range platformConfigs {
		metadata[p] = opts
	}
	content := workspace.RenderSubagentText(metadata, body, "")

	lock, lerr := writerlock.Acquire(home)
	if lerr != nil {
		return errf("Failed to write subagent: %v", lerr)
	}
	werr := atomicWriteText(path, content)
	lock.Release()
	if werr != nil {
		return errf("Failed to write subagent: %v", werr)
	}

	updated := len(oldMetadata) > 0
	verb, action := "CREATE", "Added"
	if updated {
		verb, action = "UPDATE", "Updated"
	}
	fmt.Fprintf(stdout, "[%s FILE] %s\n", verb, displayPathRelativeToHome(path, home))
	fmt.Fprintf(stdout, "[SUCCESS] %s subagent '%s'.\n", action, nameClean)
	if parsed.flags["--sync"] {
		if !syncSubagentConfigs(aikitoDir, home, false, nil, false, stdout, stderr) {
			return 1
		}
		return 0
	}
	fmt.Fprintln(stdout, "Next step: aikito sync subagents")
	return 0
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
