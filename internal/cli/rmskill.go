package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-go/internal/compat"
	"github.com/mr-miles/aikito-go/internal/projectsync"
	"github.com/mr-miles/aikito-go/internal/sync"
	"github.com/mr-miles/aikito-go/internal/workspace"
)

// cmdRmSkill ports cli.py cmd_rm_skill and remove.py remove_skill.
func cmdRmSkill(verb string, args []string, stdout, stderr io.Writer, env Environment) int {
	path := verb + " skill"
	parsed, ok := parseArgparseOpts(path, args, []string{"--sync", "--force"}, []string{"--project"}, nil, 1, stderr)
	if !ok {
		return 2
	}
	if len(parsed.positionals) == 0 {
		return argparseRequired(stderr, path, "name")
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
	var projects []string
	for _, p := range strings.Split(parsed.values["--project"], ",") {
		if p = strings.TrimSpace(p); p != "" {
			projects = append(projects, p)
		}
	}
	if !removeSkill(aikitoDir, env.Home, parsed.positionals[0], projects, parsed.flags["--force"], parsed.flags["--sync"], stdout, stderr) {
		return 1
	}
	return 0
}

func removeSkill(aikitoDir, home, name string, projects []string, force, syncFlag bool, stdout, stderr io.Writer) bool {
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
	if msg := checkWorkspaceInitialized(aikitoDir); msg != "" {
		return errf("%s", msg)
	}
	if strings.TrimSpace(name) == "" {
		return errf("Skill name cannot be empty.")
	}
	nameClean := strings.TrimSpace(name)
	if msg := workspace.ValidateResourceName(nameClean, "skill"); msg != "" {
		return errf("%s", msg)
	}
	var targets []string
	for _, p := range projects {
		for _, part := range strings.Split(p, ",") {
			if part = strings.TrimSpace(part); part != "" && !containsString(targets, part) {
				targets = append(targets, part)
			}
		}
	}
	if len(targets) == 0 && workspace.IsBundledSkillName(nameClean) {
		return errf("Cannot remove bundled system skill '%s'.", nameClean)
	}
	if len(targets) > 0 {
		return removeSkillFromProjects(aikitoDir, home, nameClean, targets, syncFlag, stdout, stderr)
	}
	return removeSkillGlobally(aikitoDir, home, nameClean, force, syncFlag, stdout, stderr)
}

// planSkillUnregistration drops name from a TOML document's skills list
// with update_skills_in_toml. checkOtherKeys adds the per-key integrity
// check _remove_skill_from_projects does.
func planSkillUnregistration(original string, data map[string]any, name string, checkOtherKeys bool, skillsErr string) (string, error) {
	list, _ := data["skills"].([]any)
	var remaining []string
	for _, v := range list {
		if s := pyStr(v); s != name {
			remaining = append(remaining, s)
		}
	}
	updated := sync.UpdateSkillsInToml(original, remaining)
	newData, err := workspace.DecodeTOML([]byte(updated))
	if err != nil {
		return "", err
	}
	if checkOtherKeys {
		keys := make([]string, 0, len(data))
		for k := range data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if k != "skills" && !reflect.DeepEqual(newData[k], data[k]) {
				return "", fmt.Errorf("Semantic integrity check failed for key '%s'", k)
			}
		}
	}
	if got, _ := newData["skills"].([]any); !stringListEquals(got, remaining) {
		return "", fmt.Errorf("%s", skillsErr)
	}
	return updated, nil
}

func listHasSkill(data map[string]any, name string) bool {
	list, ok := data["skills"].([]any)
	return ok && containsAny(list, name)
}

func removeSkillFromProjects(aikitoDir, home, name string, targets []string, syncFlag bool, stdout, stderr io.Writer) bool {
	disp := func(p string) string { return displayPathRelativeToHome(p, home) }
	for _, proj := range targets {
		if !isRegularFile(filepath.Join(aikitoDir, "projects", proj, "agent.toml")) {
			fmt.Fprintf(stderr, "[ERROR] Project '%s' not found.\n", proj)
			return false
		}
	}
	type planned struct{ path, original, updated, proj string }
	var plans []planned
	var registered, notRegistered []string
	for _, proj := range targets {
		path := filepath.Join(aikitoDir, "projects", proj, "agent.toml")
		raw, err := os.ReadFile(path)
		var data map[string]any
		if err == nil {
			data, err = workspace.DecodeTOML(raw)
		}
		if err == nil {
			var strs []string
			if list, ok := data["skills"].([]any); ok {
				for _, v := range list {
					strs = append(strs, pyStr(v))
				}
			}
			if !containsString(strs, name) {
				notRegistered = append(notRegistered, proj)
				continue
			}
			registered = append(registered, proj)
			var updated string
			if updated, err = planSkillUnregistration(string(raw), data, name, true, "Semantic integrity check failed for skills list"); err == nil {
				plans = append(plans, planned{path, string(raw), updated, proj})
				continue
			}
		}
		fmt.Fprintf(stderr, "[ERROR] Failed to update configuration for project '%s': %v\n", proj, err)
		return false
	}
	if len(registered) == 0 {
		if len(targets) == 1 {
			fmt.Fprintf(stderr, "[ERROR] Skill '%s' is not registered in project '%s'.\n", name, targets[0])
		} else {
			fmt.Fprintf(stderr, "[ERROR] Skill '%s' is not registered in any of the specified projects: %s.\n", name, strings.Join(targets, ", "))
		}
		return false
	}
	for _, proj := range notRegistered {
		fmt.Fprintf(stdout, "[INFO] Skill '%s' was not registered in project '%s'.\n", name, proj)
	}
	var updates []projectsync.FileUpdate
	for _, p := range plans {
		updates = append(updates, projectsync.FileUpdate{Path: p.path, Pre: p.original, Post: p.updated})
	}
	if ok, msg := projectsync.ExecuteSelectionTransaction(home, aikitoDir, registered, updates, nil, "", []string{name}, atomicWriteText); !ok {
		fmt.Fprintf(stderr, "[ERROR] Failed to update project configuration: %s\n", msg)
		return false
	}
	for _, p := range plans {
		fmt.Fprintf(stdout, "[UPDATE FILE] %s (unregistered skill from project '%s')\n", disp(p.path), p.proj)
	}
	quoted := make([]string, len(registered))
	for i, p := range registered {
		quoted[i] = "'" + p + "'"
	}
	fmt.Fprintf(stdout, "\n[SUCCESS] Unregistered skill '%s' from project(s): %s.\n", name, strings.Join(quoted, ", "))
	if syncFlag {
		out := projectsync.Out{Stdout: stdout, Stderr: stderr}
		for _, proj := range registered {
			if !projectsync.SyncProject(out, aikitoDir, home, proj, "", false, false) {
				return false
			}
		}
	}
	return true
}

func removeSkillGlobally(aikitoDir, home, name string, force, syncFlag bool, stdout, stderr io.Writer) bool {
	disp := func(p string) string { return displayPathRelativeToHome(p, home) }
	skillDir := filepath.Join(aikitoDir, "skills", name)
	skillsToml := filepath.Join(aikitoDir, "skills.toml")
	hasCanonical := isDir(skillDir)

	var skillsUpdate *projectsync.FileUpdate
	if isRegularFile(skillsToml) {
		raw, err := os.ReadFile(skillsToml)
		var data map[string]any
		if err == nil {
			data, err = workspace.DecodeTOML(raw)
		}
		if err == nil && listHasSkill(data, name) {
			var updated string
			if updated, err = planSkillUnregistration(string(raw), data, name, false, "Semantic check failed for skills.toml"); err == nil {
				skillsUpdate = &projectsync.FileUpdate{Path: skillsToml, Pre: string(raw), Post: updated}
			}
		}
		if err != nil {
			fmt.Fprintf(stderr, "[ERROR] Failed to read global skills configuration: %v\n", err)
			return false
		}
	}
	if !hasCanonical && skillsUpdate == nil {
		fmt.Fprintf(stderr, "[ERROR] Skill '%s' does not exist in workspace (%s).\n", name, disp(aikitoDir))
		return false
	}

	type planned struct{ path, original, updated, proj string }
	var plans []planned
	var referencing []string
	entries, _ := os.ReadDir(filepath.Join(aikitoDir, "projects"))
	for _, e := range entries {
		projDir := filepath.Join(aikitoDir, "projects", e.Name())
		cfg := filepath.Join(projDir, "agent.toml")
		if !isDir(projDir) || !isRegularFile(cfg) {
			continue
		}
		raw, err := os.ReadFile(cfg)
		var data map[string]any
		if err == nil {
			data, err = workspace.DecodeTOML(raw)
		}
		if err == nil && listHasSkill(data, name) {
			referencing = append(referencing, e.Name())
			var updated string
			if updated, err = planSkillUnregistration(string(raw), data, name, false, "Semantic check failed for "+e.Name()); err == nil {
				plans = append(plans, planned{cfg, string(raw), updated, e.Name()})
			}
		}
		if err != nil {
			fmt.Fprintf(stderr, "[WARN] Failed to inspect configuration for project '%s': %v\n", e.Name(), err)
		}
	}
	if len(referencing) > 0 && !force {
		quoted := make([]string, len(referencing))
		for i, p := range referencing {
			quoted[i] = "'" + p + "'"
		}
		fmt.Fprintf(stderr, "[ERROR] Skill '%s' is still registered in project(s): %s.\n"+
			"Unregister it first with 'aikito rm skill %s --project %s', or use --force to unregister from all projects and delete.\n",
			name, strings.Join(quoted, ", "), name, strings.Join(referencing, ","))
		return false
	}

	var updates []projectsync.FileUpdate
	for _, p := range plans {
		updates = append(updates, projectsync.FileUpdate{Path: p.path, Pre: p.original, Post: p.updated})
	}
	canonical := ""
	if hasCanonical {
		canonical = skillDir
	}
	if ok, msg := projectsync.ExecuteSelectionTransaction(home, aikitoDir, referencing, updates, skillsUpdate, canonical, []string{name}, atomicWriteText); !ok {
		fmt.Fprintf(stderr, "[ERROR] Failed during skill removal: %s\n", msg)
		return false
	}
	for _, p := range plans {
		fmt.Fprintf(stdout, "[UPDATE FILE] %s (unregistered skill from project '%s')\n", disp(p.path), p.proj)
	}
	if skillsUpdate != nil {
		fmt.Fprintf(stdout, "[UPDATE FILE] %s (unregistered global skill)\n", disp(skillsToml))
	}
	if hasCanonical {
		fmt.Fprintf(stdout, "[REMOVE DIR] %s\n", disp(skillDir))
	}
	fmt.Fprintf(stdout, "\n[SUCCESS] Removed skill '%s'.\n", name)

	if syncFlag {
		out := projectsync.Out{Stdout: stdout, Stderr: stderr}
		for _, p := range plans {
			if !projectsync.SyncProject(out, aikitoDir, home, p.proj, "", false, false) {
				return false
			}
		}
		if !compat.CanSymlink() {
			fmt.Fprint(stderr, symlinkRequirementGuidance)
			return false
		}
		if !syncGlobalResourcesQuiet(aikitoDir, home, stdout, stderr) {
			return false
		}
	}
	return true
}
