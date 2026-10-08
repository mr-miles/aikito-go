package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mr-miles/aikito-go/internal/compat"
	"github.com/mr-miles/aikito-go/internal/project"
	"github.com/mr-miles/aikito-go/internal/projectsync"
	"github.com/mr-miles/aikito-go/internal/registry"
	"github.com/mr-miles/aikito-go/internal/workspace"
	"github.com/mr-miles/aikito-go/internal/writerlock"
)

// bundledSkillOrder mirrors templating.py's BUNDLED_SKILL_NAMES tuple order
// exactly (not Go map iteration order, which is randomized).
var bundledSkillOrder = []string{"aikito", "durable-memory"}

func cmdInit(args []string, stdout, stderr io.Writer, env Environment) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: aikito init workspace|project ...")
		return 2
	}
	switch args[0] {
	case "workspace":
		return cmdInitWorkspace(args[1:], stdout, stderr, env)
	case "project":
		return cmdInitProject(args[1:], stdout, stderr, env)
	default:
		fmt.Fprintf(stderr, "[ERROR] Unknown init target: %s\n", args[0])
		return 2
	}
}

// --- init workspace ---

func cmdInitWorkspace(args []string, stdout, stderr io.Writer, env Environment) int {
	var workspacePathArg string
	force := false
	var extra []string
	for _, a := range args {
		switch {
		// argparse accepts any unique prefix of --force.
		case len(a) > 2 && strings.HasPrefix("--force", a):
			force = true
		case strings.HasPrefix(a, "-") && a != "-":
			extra = append(extra, a)
		case workspacePathArg == "":
			workspacePathArg = a
		default:
			extra = append(extra, a)
		}
	}
	if len(extra) > 0 {
		return argparseUnrecognized(stderr, extra)
	}

	if !compat.CanSymlink() {
		fmt.Fprintln(stderr, "[ERROR] This platform does not support symbolic links, which Aikito requires.")
		return 1
	}

	var target string
	if workspacePathArg != "" {
		target = workspacePathArg
	} else {
		dir, err := env.AikitoDir()
		if err != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", err)
			return 1
		}
		target = dir
	}
	resolvedTarget, err := workspace.ResolvePath(workspace.ExpandUser(env.Home, target))
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	existingWorkspace := isRecognizedWorkspace(resolvedTarget)

	if !initWorkspace(resolvedTarget, env.Home, force, stdout, stderr) {
		return 1
	}

	if workspacePathArg != "" {
		pointerPath, err := workspace.PersistWorkspace(resolvedTarget, env.Home, env.Env)
		if err != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "[CONFIG] Default workspace: %s\n", resolvedTarget)
		fmt.Fprintf(stdout, "[CONFIG] Workspace pointer: %s\n", pointerPath)
	}

	// cmd_init's next-step hint.
	plan, err := buildAdoptPlan(resolvedTarget, env.Home, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	adoption := summarizeAdoptPlan(plan)
	switch {
	case adoption.totalChanges() > 0 || adoption.Conflicts > 0 || adoption.Errors > 0:
		fmt.Fprintln(stdout, "\nNext step: Run 'aikito adopt'. It checks the complete import plan before changing the workspace.")
	case existingWorkspace:
		fmt.Fprintln(stdout, "\nNext step: Run 'aikito doctor' to check this workspace against the Agents and paths available on this host.")
	case len(detectExistingAgents(env.Home)) > 0:
		fmt.Fprintln(stdout, "\nNext step: Run 'aikito sync'. It checks the complete plan for conflicts before changing managed configuration on this host.")
	default:
		fmt.Fprintln(stdout, "\n[INFO] No supported Agents detected on this host. The workspace is ready; synchronization can wait until an Agent is installed.")
	}
	return 0
}

// targetValidationError ports init.py's _target_validation_error. The
// "inside the CLI source tree" check has no meaning for a compiled binary;
// the source-checkout marker check is kept.
func targetValidationError(target string) string {
	info, err := os.Stat(target)
	if err != nil {
		return ""
	}
	if !info.IsDir() {
		return fmt.Sprintf("Target path exists but is not a directory: %s", target)
	}
	entries, _ := os.ReadDir(target)
	if len(entries) == 0 {
		return ""
	}
	source := true
	for _, m := range []string{"LICENSE", "README.md", "pyproject.toml"} {
		if _, err := os.Stat(filepath.Join(target, m)); err != nil {
			source = false
		}
	}
	if source {
		return fmt.Sprintf("Target looks like an Aikito source checkout. Keep the CLI source and workspace in separate directories: %s", target)
	}
	if isRecognizedWorkspace(target) {
		return ""
	}
	return fmt.Sprintf("Target directory is not empty and is not a recognized Aikito workspace: %s", target)
}

// isRecognizedWorkspace mirrors init.py's is_recognized_workspace: the
// minimal structural check for "this directory already looks like an
// Aikito workspace" (used to decide CREATE vs CONNECT/REFRESH messaging),
// independent of RequireCurrentLayout's stricter validation.
func isRecognizedWorkspace(target string) bool {
	if _, err := os.Stat(filepath.Join(target, "skills.toml")); err != nil {
		return false
	}
	for _, dir := range []string{"mcps", "memory", "projects", "skills", "global"} {
		info, err := os.Stat(filepath.Join(target, dir))
		if err != nil || !info.IsDir() {
			return false
		}
	}
	if info, err := os.Stat(filepath.Join(target, "layout.toml")); err == nil && info.Mode().IsRegular() {
		return true
	}
	agentsOK := fileExists(filepath.Join(target, "agents.toml"))
	subagentsOK := fileExists(filepath.Join(target, "subagents.toml"))
	return agentsOK && subagentsOK
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func initWorkspace(target, home string, force bool, stdout, stderr io.Writer) bool {
	existingWorkspace := isRecognizedWorkspace(target)

	if err := requireLayoutLikePython(target); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return false
	}
	if msg := targetValidationError(target); msg != "" {
		fmt.Fprintf(stderr, "[ERROR] %s\n", msg)
		return false
	}

	detected := detectExistingAgents(home)

	switch {
	case existingWorkspace && force:
		fmt.Fprintf(stdout, "[INFO] Refreshing Aikito workspace templates in: %s\n", target)
	case existingWorkspace:
		fmt.Fprintf(stdout, "[INFO] Connecting to existing Aikito workspace: %s\n", target)
	default:
		fmt.Fprintf(stdout, "[INFO] Initializing Aikito workspace in: %s\n", target)
	}

	dirsToCreate := []string{
		target,
		filepath.Join(target, "global"),
		filepath.Join(target, "projects"),
		filepath.Join(target, "mcps"),
		filepath.Join(target, "skills"),
		filepath.Join(target, "subagents"),
		filepath.Join(target, "agents"),
		filepath.Join(target, "memory", "notes"),
	}
	for _, d := range dirsToCreate {
		if _, err := os.Stat(d); err != nil {
			if err := os.MkdirAll(d, 0o777); err != nil {
				fmt.Fprintf(stderr, "[ERROR] %v\n", err)
				return false
			}
			fmt.Fprintf(stdout, "[CREATE DIR] %s\n", d)
		}
	}

	// TODO(resourcewrite pipeline): once internal/sync's resource-write
	// path exists, route these writes through it (atomic write + proper
	// workspace-resource bookkeeping) instead of writing files directly.
	if !writeWorkspaceTemplateFiles(target, detected, force, existingWorkspace, stdout, stderr) {
		return false
	}

	for _, skillName := range bundledSkillOrder {
		dest := filepath.Join(target, "skills", skillName)
		if _, err := os.Stat(dest); err == nil {
			if force || !existingWorkspace {
				fmt.Fprintf(stdout, "[SKIP DIR] %s (Already exists)\n", dest)
			}
			continue
		}
		if err := copyEmbeddedDir(fmt.Sprintf("skills/%s", skillName), dest); err != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", err)
			return false
		}
		fmt.Fprintf(stdout, "[CREATE DIR] %s (Bundled %s skill)\n", dest, skillName)
	}

	if existingWorkspace {
		lock, err := writerlock.Acquire(home)
		if err != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", err)
			return false
		}
		_, rerr := executeBundledRefresh(planBundledRefresh(target, true), target, home, false, stdout)
		lock.Release()
		if rerr != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", rerr)
			return false
		}
	}

	gitDir := filepath.Join(target, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		if gitBin, lerr := exec.LookPath("git"); lerr == nil {
			cmd := exec.Command(gitBin, "init", target)
			if out, rerr := cmd.CombinedOutput(); rerr != nil {
				fmt.Fprintf(stderr, "[ERROR] Failed to run 'git init': %s\n", strings.TrimSpace(string(out)))
				return false
			}
			fmt.Fprintf(stdout, "[GIT INIT] Initialized Git repository in %s\n", target)
		} else {
			fmt.Fprintln(stderr, "[ERROR] 'git' executable not found in PATH.")
			return false
		}
	} else if !existingWorkspace {
		fmt.Fprintf(stdout, "[SKIP GIT] Git repository already exists in %s\n", target)
	}

	switch {
	case existingWorkspace && force:
		fmt.Fprintln(stdout, "\n[SUCCESS] Existing Aikito workspace templates refreshed!")
	case existingWorkspace:
		fmt.Fprintln(stdout, "\n[CONNECTED] Existing Aikito workspace is ready on this host.")
	default:
		fmt.Fprintln(stdout, "\n[SUCCESS] Aikito workspace initialization complete!")
	}

	if len(detected) > 0 {
		fmt.Fprintln(stdout, "\n[INFO] Detected installed Agent(s):")
		for _, d := range detected {
			fmt.Fprintf(stdout, "  - %s (%s)\n", d.displayName, d.evidencePath)
		}
	}

	return true
}

type detectedAgent struct {
	name         string
	displayName  string
	evidencePath string
}

// detectExistingAgents mirrors templating.py's detect_existing_agents: for
// each built-in agent (in registry order), check installation via the
// already-built tri-state availability detector, and report either the
// resolved executable path (if found via PATH) or the first existing
// detect-marker path under home, falling back to home itself.
func detectExistingAgents(home string) []detectedAgent {
	var out []detectedAgent
	for _, name := range registry.BuiltinAgents {
		agent, err := registry.BundledAgent(name, home)
		if err != nil {
			continue
		}
		avail := registry.CheckAgentAvailabilityForAgent(agent, home, nil)
		if !avail.IsInstalled() {
			continue
		}
		evidence := home
		if agent.Detect != nil {
			for _, command := range agent.Detect.Commands {
				if p, err := exec.LookPath(command); err == nil {
					evidence = p
					break
				}
			}
			if evidence == home {
				for _, p := range agent.Detect.Paths {
					full := filepath.Join(home, filepath.FromSlash(p))
					if _, err := os.Stat(full); err == nil {
						evidence = full
						break
					}
				}
			}
		}
		out = append(out, detectedAgent{name: name, displayName: agent.DisplayName, evidencePath: evidence})
	}
	return out
}

// writeWorkspaceTemplateFiles mirrors templating.py's render_workspace_files
// + init.py's per-file write-if-absent-or-force loop, plus writing one
// agents/<name>.toml per detected agent (the v2 per-file layout — there is
// no "_join_agent_templates" monolithic-file step in the current,
// post-migration init path).
func writeWorkspaceTemplateFiles(target string, detected []detectedAgent, force, existingWorkspace bool, stdout, stderr io.Writer) bool {
	type fileSpec struct {
		dest, content, desc string
	}
	var files []fileSpec

	templateFiles := []struct{ dest, templateName, desc string }{
		{"config.toml", "config.toml", "Workspace config template"},
		{"skills.toml", "skills.toml", "Global skills config"},
		{filepath.Join("global", "AGENTS.md"), "global/AGENTS.md", "Global agent instructions"},
		{".gitignore", "gitignore", "Workspace .gitignore with leading slashes"},
		{"layout.toml", "", "Workspace resource layout version"},
	}
	for _, tf := range templateFiles {
		// render_workspace_files puts the detected agents' definitions
		// just before layout.toml.
		if tf.dest == "layout.toml" {
			for _, d := range detected {
				content, err := registry.BundledAgentTemplateText(d.name)
				if err != nil {
					fmt.Fprintf(stderr, "[ERROR] %v\n", err)
					return false
				}
				files = append(files, fileSpec{
					dest:    filepath.Join(target, "agents", d.name+".toml"),
					content: content,
					desc:    fmt.Sprintf("%s Agent definition", d.name),
				})
			}
		}
		content := workspace.LayoutContent
		if tf.templateName != "" {
			c, err := loadTemplate(tf.templateName)
			if err != nil {
				fmt.Fprintf(stderr, "[ERROR] %v\n", err)
				return false
			}
			content = c
		}
		files = append(files, fileSpec{dest: filepath.Join(target, tf.dest), content: content, desc: tf.desc})
	}

	for _, f := range files {
		_, statErr := os.Stat(f.dest)
		exists := statErr == nil
		if !exists || force {
			if err := os.MkdirAll(filepath.Dir(f.dest), 0o777); err != nil {
				fmt.Fprintf(stderr, "[ERROR] %v\n", err)
				return false
			}
			if err := os.WriteFile(f.dest, []byte(f.content), 0o644); err != nil {
				fmt.Fprintf(stderr, "[ERROR] %v\n", err)
				return false
			}
			tag := "[CREATE FILE]"
			if force && exists {
				tag = "[FORCE WRITE]"
			}
			fmt.Fprintf(stdout, "%s %s (%s)\n", tag, f.dest, f.desc)
		} else if !existingWorkspace {
			fmt.Fprintf(stdout, "[SKIP FILE] %s (Already exists)\n", f.dest)
		}
	}
	return true
}

func copyEmbeddedDir(embeddedDir, dest string) error {
	entries, err := templatesFS.ReadDir("templates/" + embeddedDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0o777); err != nil {
		return err
	}
	for _, e := range entries {
		srcChild := embeddedDir + "/" + e.Name()
		destChild := filepath.Join(dest, e.Name())
		if e.IsDir() {
			if err := copyEmbeddedDir(srcChild, destChild); err != nil {
				return err
			}
			continue
		}
		data, err := templatesFS.ReadFile("templates/" + srcChild)
		if err != nil {
			return err
		}
		if err := os.WriteFile(destChild, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// --- init project ---

func cmdInitProject(args []string, stdout, stderr io.Writer, env Environment) int {
	var positional, extra []string
	description := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--description":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "[ERROR] --description requires a value")
				return 2
			}
			description = args[i]
		case strings.HasPrefix(a, "--description="):
			description = strings.TrimPrefix(a, "--description=")
		case strings.HasPrefix(a, "-") && a != "-":
			extra = append(extra, a)
		case len(positional) < 2:
			positional = append(positional, a)
		default:
			extra = append(extra, a)
		}
	}
	// Python rejects anything else (e.g. --dry-run) before doing any work.
	if len(extra) > 0 {
		return argparseUnrecognized(stderr, extra)
	}
	var projectName, projectPathArg string
	if len(positional) > 0 {
		projectName = positional[0]
	}
	if len(positional) > 1 {
		projectPathArg = positional[1]
	}

	if !compat.CanSymlink() {
		fmt.Fprintln(stderr, "[ERROR] This platform does not support symbolic links, which Aikito requires.")
		return 1
	}

	projectPath := env.Cwd
	if projectPathArg != "" {
		projectPath = projectPathArg
	}
	resolvedProjectPath, err := workspace.ResolvePath(workspace.ExpandUser(env.Home, projectPath))
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	aikitoDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	resolvedName := projectName
	if resolvedName == "" {
		resolvedName = filepath.Base(resolvedProjectPath)
	}

	if msg := validateInitProject(aikitoDir, env.Home, resolvedName, resolvedProjectPath); msg != "" {
		fmt.Fprintf(stderr, "[ERROR] %s\n", msg)
		return 1
	}

	projectDir := filepath.Join(aikitoDir, "projects", resolvedName)
	notesDir := filepath.Join(projectDir, "memory", "notes")
	if err := os.MkdirAll(notesDir, 0o777); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	configPath := filepath.Join(projectDir, "agent.toml")
	if _, err := os.Stat(configPath); err != nil {
		displayPath := displayPathRelativeToHome(resolvedProjectPath, env.Home)
		var lines []string
		lines = append(lines, fmt.Sprintf("name = %q", resolvedName))
		if strings.TrimSpace(description) != "" {
			descJSON, _ := json.Marshal(strings.TrimSpace(description))
			lines = append(lines, fmt.Sprintf("description = %s", descJSON))
		}
		// Matches Python's exact (unescaped) f'path = "{display_path}"'
		// interpolation verbatim, including its lack of quote-escaping for
		// a pathologically quote-containing path.
		lines = append(lines, fmt.Sprintf(`path = "%s"`, displayPath))
		lines = append(lines, fmt.Sprintf("sync_mode = %q", project.DefaultSyncMode))
		lines = append(lines, "skills = []")
		content := strings.Join(lines, "\n") + "\n"
		if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "[CREATE FILE] %s\n", configPath)
	} else {
		fmt.Fprintf(stdout, "[SKIP FILE] %s (Already exists)\n", configPath)
	}

	agentsMD := filepath.Join(projectDir, "AGENTS.md")
	if _, err := os.Stat(agentsMD); err != nil {
		content, lerr := loadTemplate("project/AGENTS.md")
		if lerr != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", lerr)
			return 1
		}
		if err := os.WriteFile(agentsMD, []byte(content), 0o644); err != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "[CREATE FILE] %s\n", agentsMD)
	} else {
		fmt.Fprintf(stdout, "[SKIP FILE] %s (Already exists)\n", agentsMD)
	}

	fmt.Fprintf(stdout, "[SUCCESS] Project '%s' initialized in Aikito workspace.\n", resolvedName)

	// As in Python's cmd_init_project, finish with a project sync so the
	// checkout's .agents/ runtime exists straight away.
	// Python passes the unresolved project path through; sync_project
	// resolves it and reports the resolved path.
	return runProjectSync(resolvedName, projectPath, false, false, stdout, stderr, env)
}

// validateInitProject is init.py's project_validation_error (registration
// validation, where every pre-existing runtime entry is foreign). Returns
// "" if valid, else a user-facing error message.
func validateInitProject(aikitoDir, home, projectName, projectPath string) string {
	return projectsync.ValidationError(aikitoDir, projectName, projectPath, home, true)
}

// displayPathRelativeToHome is a simplified stand-in for compat.py's
// safe_relative_path: "~/..." when path is under home, else the absolute
// path. Good enough for this display-only use (an agent.toml `path =`
// value a human/tool re-reads via project.ResolveProjectPath, which already
// handles both forms); not a byte-exact port of every compat.py edge case
// (e.g. case-insensitive filesystem prefix matching).
func displayPathRelativeToHome(path, home string) string {
	if home == "" {
		return path
	}
	rel, err := filepath.Rel(home, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	if rel == "." {
		return "~"
	}
	return "~/" + filepath.ToSlash(rel)
}
