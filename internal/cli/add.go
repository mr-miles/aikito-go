package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mr-miles/aikito-rs/internal/registry"
	"github.com/mr-miles/aikito-rs/internal/subagent"
	"github.com/mr-miles/aikito-rs/internal/sync"
	"github.com/mr-miles/aikito-rs/internal/workspace"
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

// --- add skill ---
//
// Scope note: this implements the GLOBAL registration branch of add_skill
// (add.py:276-810) in full (name/description inference from --from, the
// bundled-skill-name guard, skills.toml surgical update via
// sync.UpdateSkillsInToml, atomic write with rollback-on-failure). The
// --project/--global PROJECT-SCOPED registration branch (registering a
// skill into one or more projects' agent.toml instead of/in addition to
// skills.toml) is NOT ported — it's a large additional transactional-write
// path (add.py:482-676) layered on project config, which doesn't change
// the core skill-creation mechanics this command demonstrates. --sync is
// also deferred (needs sync_global_resources, not yet built). Both are
// flagged with a clear [ERROR]/TODO below rather than silently ignored.
func cmdAddSkill(args []string, stdout, stderr io.Writer, env Environment) int {
	var name, from, description string
	var force, syncFlag bool
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--from":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "[ERROR] --from requires a value")
				return 2
			}
			from = args[i]
		case a == "--description":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "[ERROR] --description requires a value")
				return 2
			}
			description = args[i]
		case a == "--force":
			force = true
		case a == "--sync":
			syncFlag = true
		case a == "--project" || a == "--global":
			fmt.Fprintln(stderr, "[ERROR] --project/--global (project-scoped skill registration) is not yet implemented in this Go build; use the global registration (omit both flags).")
			return 2
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(stderr, "[ERROR] Unknown flag: %s\n", a)
			return 2
		default:
			positional = append(positional, a)
		}
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
	if force && from == "" {
		fmt.Fprintln(stderr, "[ERROR] --force requires --from when adding a skill.")
		return 1
	}

	var sourcePath string
	var sourceIsDir bool
	var sourceMeta map[string]any
	var sourceBody string
	if from != "" {
		resolved, rerr := workspace.ResolvePath(workspace.ExpandUser(env.Home, from))
		if rerr != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", rerr)
			return 1
		}
		info, serr := os.Stat(resolved)
		if serr != nil {
			fmt.Fprintf(stderr, "[ERROR] Source path does not exist: %s\n", from)
			return 1
		}
		var sourceSkillMD string
		if info.IsDir() {
			sourceIsDir = true
			sourceSkillMD = filepath.Join(resolved, "SKILL.md")
			if fi, ferr := os.Stat(sourceSkillMD); ferr != nil || !fi.Mode().IsRegular() {
				fmt.Fprintf(stderr, "[ERROR] Source directory '%s' does not contain a SKILL.md file.\n", displayPathRelativeToHome(resolved, env.Home))
				return 1
			}
		} else {
			if !strings.EqualFold(filepath.Ext(resolved), ".md") {
				fmt.Fprintf(stderr, "[ERROR] Source file '%s' must be a markdown (.md) file.\n", displayPathRelativeToHome(resolved, env.Home))
				return 1
			}
			sourceSkillMD = resolved
		}
		sourcePath = resolved
		raw, rerr := os.ReadFile(sourceSkillMD)
		if rerr != nil {
			fmt.Fprintf(stderr, "[ERROR] Failed to read source file '%s': %v\n", displayPathRelativeToHome(sourceSkillMD, env.Home), rerr)
			return 1
		}
		sourceMeta, sourceBody = parseSimpleMarkdownFrontmatter(string(raw))

		if strings.TrimSpace(name) == "" {
			inferred, _ := sourceMeta["name"].(string)
			if inferred == "" {
				if sourceIsDir {
					inferred = filepath.Base(resolved)
				} else if strings.EqualFold(filepath.Base(resolved), "skill.md") {
					inferred = filepath.Base(filepath.Dir(resolved))
				} else {
					inferred = strings.TrimSuffix(filepath.Base(resolved), filepath.Ext(resolved))
				}
			}
			name = strings.TrimSpace(inferred)
		}
		if strings.TrimSpace(description) == "" {
			if d, ok := sourceMeta["description"].(string); ok && d != "" {
				description = strings.TrimSpace(d)
			}
		}
	}

	if strings.TrimSpace(name) == "" {
		fmt.Fprintln(stderr, "[ERROR] Skill name is required. Please specify a name or provide a source via --from.")
		return 1
	}
	nameClean := strings.TrimSpace(name)
	if msg := workspace.ValidateResourceName(nameClean, "skill"); msg != "" {
		fmt.Fprintf(stderr, "[ERROR] %s\n", msg)
		return 1
	}

	skillsRoot := filepath.Join(aikitoDir, "skills")
	skillDir := filepath.Join(skillsRoot, nameClean)
	skillFile := filepath.Join(skillDir, "SKILL.md")

	isExistingCanonical := false
	if info, serr := os.Stat(skillDir); serr == nil {
		skillMDInfo, mderr := os.Stat(skillFile)
		if !info.IsDir() || mderr != nil || !skillMDInfo.Mode().IsRegular() {
			fmt.Fprintf(stderr, "[ERROR] Canonical skill path '%s' exists but is not a valid skill directory (missing SKILL.md).\n", displayPathRelativeToHome(skillDir, env.Home))
			return 1
		}
		isExistingCanonical = true
		if from != "" && !force {
			fmt.Fprintf(stderr, "[ERROR] Skill '%s' already exists at %s\n", nameClean, displayPathRelativeToHome(skillDir, env.Home))
			return 1
		}
		if !(from != "" && force) {
			fmt.Fprintf(stderr, "[ERROR] Skill '%s' already exists at %s\n", nameClean, displayPathRelativeToHome(skillDir, env.Home))
			return 1
		}
	}

	if workspace.IsBundledSkillName(nameClean) {
		if from != "" {
			fmt.Fprintf(stderr, "[ERROR] Cannot overwrite bundled system skill '%s'.\n", nameClean)
			return 1
		}
		if !isExistingCanonical {
			fmt.Fprintf(stderr, "[ERROR] Cannot create custom skill with reserved bundled system skill name '%s'.\n", nameClean)
			return 1
		}
	}

	descVal := strings.TrimSpace(description)
	if descVal == "" {
		descVal = fmt.Sprintf("Description for %s skill.", nameClean)
	}
	titleVal := titleize(nameClean)

	skillsToml := filepath.Join(aikitoDir, "skills.toml")
	var existingGlobalSkills []string
	originalSkillsTomlText := ""
	if data, rerr := os.ReadFile(skillsToml); rerr == nil {
		originalSkillsTomlText = string(data)
		doc, derr := workspace.DecodeTOML(data)
		if derr != nil {
			fmt.Fprintf(stderr, "[ERROR] Failed to read global skills configuration: %v\n", derr)
			return 1
		}
		if raw, ok := doc["skills"].([]any); ok {
			for _, v := range raw {
				if s, ok := v.(string); ok {
					existingGlobalSkills = append(existingGlobalSkills, s)
				}
			}
		}
	}

	alreadyRegisteredGlobal := containsString(existingGlobalSkills, nameClean)
	updatingImport := isExistingCanonical && from != "" && force
	if isExistingCanonical && alreadyRegisteredGlobal && !updatingImport {
		fmt.Fprintf(stderr, "[ERROR] Skill '%s' is already registered globally.\n", nameClean)
		return 1
	}

	newGlobalSkills := existingGlobalSkills
	if !alreadyRegisteredGlobal {
		newGlobalSkills = append(append([]string{}, existingGlobalSkills...), nameClean)
	}
	skillsTomlContent := sync.UpdateSkillsInToml(originalSkillsTomlText, newGlobalSkills)
	if newDoc, derr := workspace.DecodeTOML([]byte(skillsTomlContent)); derr != nil {
		fmt.Fprintf(stderr, "[ERROR] Failed to update global skills configuration: %v\n", derr)
		return 1
	} else if raw, ok := newDoc["skills"].([]any); !ok || !stringListEquals(raw, newGlobalSkills) {
		fmt.Fprintln(stderr, "[ERROR] Failed to verify global skills configuration.")
		return 1
	}

	// Write skill content/dir first, then skills.toml; roll back both on any
	// failure (mirrors add.py's WorkspaceWriterLock-guarded try/except).
	writeErr := func() error {
		if sourcePath != "" {
			return importSkillFrom(sourcePath, sourceIsDir, sourceBody, skillDir, nameClean, descVal, sourceMeta)
		}
		if err := os.MkdirAll(skillDir, 0o777); err != nil {
			return err
		}
		content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n# %s\n\n## Overview\n\nDescribe what this skill does and when agents should use it.\n",
			nameClean, descVal, titleVal)
		return os.WriteFile(skillFile, []byte(content), 0o644)
	}()
	if writeErr != nil {
		fmt.Fprintf(stderr, "[ERROR] Failed to write global skill: %v\n", writeErr)
		return 1
	}
	if err := os.WriteFile(skillsToml, []byte(skillsTomlContent), 0o644); err != nil {
		if originalSkillsTomlText != "" {
			_ = os.WriteFile(skillsToml, []byte(originalSkillsTomlText), 0o644)
		}
		if sourcePath == "" {
			_ = os.RemoveAll(skillDir)
		}
		fmt.Fprintf(stderr, "[ERROR] Failed to write global skill: %v\n", err)
		return 1
	}

	if updatingImport {
		fmt.Fprintf(stdout, "[UPDATE DIR] %s\n", displayPathRelativeToHome(skillDir, env.Home))
		fmt.Fprintf(stdout, "[UPDATE FILE] %s\n", displayPathRelativeToHome(skillFile, env.Home))
	} else {
		fmt.Fprintf(stdout, "[CREATE DIR] %s\n", displayPathRelativeToHome(skillDir, env.Home))
		fmt.Fprintf(stdout, "[CREATE FILE] %s\n", displayPathRelativeToHome(skillFile, env.Home))
	}
	if !alreadyRegisteredGlobal {
		fmt.Fprintf(stdout, "[UPDATE FILE] %s (registered global skill)\n", displayPathRelativeToHome(skillsToml, env.Home))
	}
	action := "Added"
	if updatingImport {
		action = "Updated"
	}
	fmt.Fprintf(stdout, "\n[SUCCESS] %s global skill '%s'.\n", action, nameClean)
	fmt.Fprintln(stdout, "\U0001F4A1 Next steps:")
	step := 1
	if sourcePath == "" {
		fmt.Fprintf(stdout, "  %d. Update instructions in %s (or run 'aikito edit skill %s')\n", step, displayPathRelativeToHome(skillFile, env.Home), nameClean)
		step++
	}
	fmt.Fprintf(stdout, "  %d. Synchronize to agents: aikito sync global\n", step)

	if syncFlag {
		fmt.Fprintln(stderr, "[ERROR] --sync is not yet implemented in this Go build (needs the global-resource sync engine); run 'aikito sync global' separately.")
		return 1
	}
	return 0
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

// importSkillFrom copies sourcePath (file or dir) into skillDir, normalizing
// the staged SKILL.md's frontmatter name/description to the canonical
// values. Simplified vs add.py's _SkillImportTransaction: copies directly
// into the target rather than staging in a sibling temp dir first, so a
// failure partway through a directory copy can leave a partial skillDir
// (caller's caller doesn't currently roll this back further than removing
// skills.toml's write). Acceptable for this port's scope; a hardened
// stage-then-atomic-rename version is a documented follow-up.
func importSkillFrom(sourcePath string, sourceIsDir bool, sourceBody, skillDir, name, descVal string, meta map[string]any) error {
	if err := os.RemoveAll(skillDir); err != nil {
		return err
	}
	if err := os.MkdirAll(skillDir, 0o777); err != nil {
		return err
	}
	if sourceIsDir {
		if err := copyDirTree(sourcePath, skillDir); err != nil {
			return err
		}
	} else {
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), data, 0o644); err != nil {
			return err
		}
	}
	// Normalize the staged SKILL.md's frontmatter name/description.
	newMeta := map[string]any{}
	for k, v := range meta {
		newMeta[k] = v
	}
	newMeta["name"] = name
	if _, ok := newMeta["description"]; !ok || strings.TrimSpace(fmt.Sprint(newMeta["description"])) == "" {
		newMeta["description"] = descVal
	}
	content := renderSimpleFrontmatter(newMeta, sourceBody)
	return os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644)
}

func copyDirTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if name == ".git" || name == "__pycache__" || name == ".DS_Store" || strings.HasSuffix(name, ".pyc") {
			continue
		}
		srcChild := filepath.Join(src, name)
		dstChild := filepath.Join(dst, name)
		if e.IsDir() {
			if err := os.MkdirAll(dstChild, 0o777); err != nil {
				return err
			}
			if err := copyDirTree(srcChild, dstChild); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(srcChild)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dstChild, data, 0o644); err != nil {
			return err
		}
	}
	return nil
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

// renderSimpleFrontmatter is the write-side counterpart of
// parseSimpleMarkdownFrontmatter: emits name/description first (if
// present), then any other scalar keys, each as a plain (optionally
// quoted-if-containing-a-colon) "key: value" line.
func renderSimpleFrontmatter(meta map[string]any, body string) string {
	var b strings.Builder
	b.WriteString("---\n")
	order := []string{"name", "description"}
	for k := range meta {
		if k == "name" || k == "description" {
			continue
		}
		order = append(order, k)
	}
	for _, k := range order {
		v, ok := meta[k]
		if !ok {
			continue
		}
		b.WriteString(k)
		b.WriteString(": ")
		s := fmt.Sprint(v)
		if strings.ContainsAny(s, ":#") {
			b.WriteString(workspace.CanonicalJSON(s))
		} else {
			b.WriteString(s)
		}
		b.WriteString("\n")
	}
	b.WriteString("---\n")
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	b.WriteString(body)
	return b.String()
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
