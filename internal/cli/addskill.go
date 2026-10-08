package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/mr-miles/aikito-go/internal/compat"
	"github.com/mr-miles/aikito-go/internal/project"
	"github.com/mr-miles/aikito-go/internal/projectsync"
	"github.com/mr-miles/aikito-go/internal/sync"
	"github.com/mr-miles/aikito-go/internal/workspace"
	"github.com/mr-miles/aikito-go/internal/writerlock"
)

// cmdAddSkill ports cli.py cmd_add_skill and add.py add_skill /
// _add_skill_impl: create or import a canonical skill and register it
// globally (skills.toml) or in projects (agent.toml), optionally syncing.
func cmdAddSkill(args []string, stdout, stderr io.Writer, env Environment) int {
	parsed, ok := parseArgparseOpts("add skill", args,
		[]string{"--global", "--sync", "--force"},
		[]string{"--from", "--description", "--project"}, nil, 1, stderr)
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

	projectArg, hasProject := parsed.values["--project"]
	isGlobal := parsed.flags["--global"]
	if projectArg != "" && isGlobal {
		fmt.Fprintln(stderr, "[ERROR] Cannot specify both --project and --global.")
		return 1
	}
	var projects []string
	if projectArg != "" {
		for _, p := range strings.Split(projectArg, ",") {
			if p = strings.TrimSpace(p); p != "" {
				projects = append(projects, p)
			}
		}
	} else if !isGlobal {
		detected, derr := project.DetectCurrentProject(aikitoDir, env.Cwd, env.Home)
		var conflict *project.ContextConflictError
		if errors.As(derr, &conflict) {
			fmt.Fprintf(stderr, "[CONFLICT] Multiple projects match current directory '%s': %s\n", conflict.Path, strings.Join(conflict.Projects, ", "))
			return 1
		}
		if detected != "" {
			fmt.Fprintf(stdout, "[aikito] Target project: '%s' (detected from cwd)\n", detected)
			projects = []string{detected}
		}
	}
	_ = hasProject

	var name, description *string
	if len(parsed.positionals) > 0 {
		name = &parsed.positionals[0]
	}
	if d, ok := parsed.values["--description"]; ok {
		description = &d
	}
	var from *string
	if f, ok := parsed.values["--from"]; ok {
		from = &f
	}
	opts := addSkillOptions{
		name: name, description: description, projects: projects, from: from,
		sync: parsed.flags["--sync"], force: parsed.flags["--force"],
	}
	if from != nil {
		opts.fromPath = resolveAgainstCwd(env, *from)
	}
	if opts.sync {
		lock, lerr := writerlock.Acquire(env.Home)
		if lerr != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", lerr)
			return 1
		}
		defer lock.Release()
	}
	if !addSkillImpl(aikitoDir, env.Home, opts, stdout, stderr) {
		return 1
	}
	return 0
}

type addSkillOptions struct {
	name, description, from *string
	fromPath                string // from, made absolute against the command's cwd
	projects                []string
	sync, force             bool
}

func strOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

const skillSkeletonFormat = "---\nname: %s\ndescription: %s\n---\n\n# %s\n\n## Overview\n\nDescribe what this skill does and when agents should use it.\n"

func addSkillImpl(aikitoDir, home string, o addSkillOptions, stdout, stderr io.Writer) bool {
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
	if o.force && o.from == nil {
		return errf("--force requires --from when adding a skill.")
	}

	var targetProjects []string
	for _, p := range o.projects {
		for _, sub := range strings.Split(p, ",") {
			if sub = strings.TrimSpace(sub); sub != "" && !containsString(targetProjects, sub) {
				targetProjects = append(targetProjects, sub)
			}
		}
	}
	for _, proj := range targetProjects {
		if !isRegularFile(filepath.Join(aikitoDir, "projects", proj, "agent.toml")) {
			return errf("Project '%s' not found.", proj)
		}
	}

	name := strOrEmpty(o.name)
	description := o.description
	var sourcePath string
	sourceIsDir := false
	if o.from != nil {
		sourcePath, _ = compat.ResolvePath(o.fromPath)
		info, serr := os.Stat(sourcePath)
		if serr != nil {
			return errf("Source path does not exist: %s", *o.from)
		}
		var sourceSkillMD string
		switch {
		case info.IsDir():
			sourceIsDir = true
			sourceSkillMD = filepath.Join(sourcePath, "SKILL.md")
			if !isRegularFile(sourceSkillMD) {
				return errf("Source directory '%s' does not contain a SKILL.md file.", disp(sourcePath))
			}
		case info.Mode().IsRegular():
			if strings.ToLower(filepath.Ext(sourcePath)) != ".md" {
				return errf("Source file '%s' must be a markdown (.md) file.", disp(sourcePath))
			}
			sourceSkillMD = sourcePath
		default:
			return errf("Source path is not a file or directory: %s", *o.from)
		}
		raw, rerr := os.ReadFile(sourceSkillMD)
		if rerr != nil {
			return errf("Failed to read source file '%s': %v", disp(sourceSkillMD), rerr)
		}
		meta, _ := workspace.ParseMarkdownFrontmatter(string(raw), nil)
		if strings.TrimSpace(name) == "" {
			inferred := ""
			if v, ok := meta["name"]; ok && pyTruthy(v) {
				inferred = pyStr(v)
			} else if sourceIsDir {
				inferred = filepath.Base(sourcePath)
			} else {
				stem := strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath))
				if strings.ToLower(stem) == "skill" {
					inferred = filepath.Base(filepath.Dir(sourcePath))
				} else {
					inferred = stem
				}
			}
			name = strings.TrimSpace(inferred)
		}
		if description == nil || strings.TrimSpace(*description) == "" {
			if v, ok := meta["description"]; ok && pyTruthy(v) {
				d := strings.TrimSpace(pyStr(v))
				description = &d
			}
		}
	}

	if strings.TrimSpace(name) == "" {
		return errf("Skill name is required. Please specify a name or provide a source via --from.")
	}
	nameClean := strings.TrimSpace(name)
	if msg := workspace.ValidateResourceName(nameClean, "skill"); msg != "" {
		return errf("%s", msg)
	}

	skillDir := filepath.Join(aikitoDir, "skills", nameClean)
	skillFile := filepath.Join(skillDir, "SKILL.md")
	isExistingCanonical := false
	if _, err := os.Stat(skillDir); err == nil {
		if !isDir(skillDir) || !isRegularFile(skillFile) {
			return errf("Canonical skill path '%s' exists but is not a valid skill directory (missing SKILL.md).", disp(skillDir))
		}
		isExistingCanonical = true
		if o.from != nil && !o.force {
			return errf("Skill '%s' already exists at %s", nameClean, disp(skillDir))
		}
		if len(targetProjects) == 0 && !(o.from != nil && o.force) {
			return errf("Skill '%s' already exists at %s", nameClean, disp(skillDir))
		}
	}

	if workspace.IsBundledSkillName(nameClean) {
		if o.from != nil {
			return errf("Cannot overwrite bundled system skill '%s'.", nameClean)
		}
		if !isExistingCanonical {
			return errf("Cannot create custom skill with reserved bundled system skill name '%s'.", nameClean)
		}
		if len(targetProjects) > 0 {
			fmt.Fprintf(stdout, "[INFO] '%s' is a built-in skill and already active globally.\n", nameClean)
		}
	}

	descVal := strings.TrimSpace(strOrEmpty(description))
	if descVal == "" {
		descVal = fmt.Sprintf("Description for %s skill.", nameClean)
	}
	skeleton := fmt.Sprintf(skillSkeletonFormat, nameClean, descVal, titleize(nameClean))
	updatingImport := isExistingCanonical && o.from != nil && o.force

	newImport := func() (*skillImportTransaction, bool) {
		if sourcePath == "" {
			return nil, true
		}
		tx := &skillImportTransaction{
			sourcePath: sourcePath, sourceIsDir: sourceIsDir, targetDir: skillDir,
			name: nameClean, description: description, defaultDescription: descVal,
		}
		if err := tx.prepare(); err != nil {
			return nil, errf("Failed to prepare skill import: %v", err)
		}
		return tx, true
	}

	if len(targetProjects) > 0 {
		var alreadyRegistered, pending []string
		for _, proj := range targetProjects {
			data, err := loadTOMLFile(filepath.Join(aikitoDir, "projects", proj, "agent.toml"))
			if err != nil {
				return errf("Failed to read configuration for project '%s': %v", proj, err)
			}
			if list, ok := data["skills"].([]any); ok && containsAny(list, nameClean) {
				alreadyRegistered = append(alreadyRegistered, proj)
			} else {
				pending = append(pending, proj)
			}
		}
		if isExistingCanonical && len(pending) == 0 && !updatingImport {
			if len(targetProjects) == 1 {
				return errf("Skill '%s' already exists and is already registered in project '%s'", nameClean, targetProjects[0])
			}
			return errf("Skill '%s' already exists and is already registered in all specified projects: %s", nameClean, strings.Join(targetProjects, ", "))
		}

		type planned struct{ path, original, updated, proj string }
		var plans []planned
		for _, proj := range pending {
			path := filepath.Join(aikitoDir, "projects", proj, "agent.toml")
			updated, original, perr := planSkillRegistration(path, nameClean)
			if perr != nil {
				return errf("Failed to update configuration for project '%s': %v", proj, perr)
			}
			plans = append(plans, planned{path, original, updated, proj})
		}

		tx, ok := newImport()
		if !ok {
			return false
		}
		createdSkillDir := !isExistingCanonical
		lock, lerr := writerlock.Acquire(home)
		if lerr != nil {
			if tx != nil {
				tx.discard()
			}
			return errf("%v", lerr)
		}
		writeErr := func() error {
			if tx != nil {
				if err := tx.apply(); err != nil {
					return err
				}
			} else if !isExistingCanonical {
				if err := os.MkdirAll(skillDir, 0o777); err != nil {
					return err
				}
				if err := atomicWriteText(skillFile, skeleton); err != nil {
					return err
				}
			}
			for _, p := range plans {
				if err := atomicWriteText(p.path, p.updated); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "[UPDATE FILE] %s (registered skill for project '%s')\n", disp(p.path), p.proj)
			}
			return nil
		}()
		if writeErr != nil {
			for _, p := range plans {
				_ = atomicWriteText(p.path, p.original)
			}
			if tx != nil {
				tx.rollback()
			} else if createdSkillDir {
				_ = os.RemoveAll(skillDir)
			}
			lock.Release()
			return errf("Failed to write skill or project configuration: %v", writeErr)
		}
		if tx != nil {
			tx.discard()
		}
		lock.Release()

		switch {
		case updatingImport:
			fmt.Fprintf(stdout, "[UPDATE DIR] %s\n[UPDATE FILE] %s\n", disp(skillDir), disp(skillFile))
		case createdSkillDir:
			fmt.Fprintf(stdout, "[CREATE DIR] %s\n[CREATE FILE] %s\n", disp(skillDir), disp(skillFile))
		default:
			fmt.Fprintf(stdout, "[INFO] Using existing canonical skill '%s' at %s\n", nameClean, disp(skillDir))
		}
		for _, proj := range alreadyRegistered {
			fmt.Fprintf(stdout, "[INFO] Skill '%s' was already registered in project '%s'.\n", nameClean, proj)
		}
		quoted := func(list []string) string {
			q := make([]string, len(list))
			for i, p := range list {
				q[i] = "'" + p + "'"
			}
			return strings.Join(q, ", ")
		}
		switch {
		case len(pending) > 0 && updatingImport:
			fmt.Fprintf(stdout, "\n[SUCCESS] Updated skill '%s' for project(s): %s.\n", nameClean, quoted(pending))
		case len(pending) > 0:
			fmt.Fprintf(stdout, "\n[SUCCESS] Added skill '%s' to project(s): %s.\n", nameClean, quoted(pending))
		default:
			fmt.Fprintf(stdout, "\n[SUCCESS] Updated skill '%s' for project(s): %s.\n", nameClean, quoted(targetProjects))
		}
		fmt.Fprintln(stdout, "\U0001F4A1 Next steps:")
		step := 1
		if createdSkillDir && sourcePath == "" {
			fmt.Fprintf(stdout, "  %d. Update instructions in %s (or run 'aikito edit skill %s')\n", step, disp(skillFile), nameClean)
			step++
		}
		syncTargets := pending
		if updatingImport {
			syncTargets = targetProjects
		}
		cmds := make([]string, len(syncTargets))
		for i, p := range syncTargets {
			cmds[i] = "aikito sync project " + p
		}
		fmt.Fprintf(stdout, "  %d. Synchronize project(s): %s\n", step, strings.Join(cmds, " && "))

		if o.sync {
			syncOK := true
			out := projectsync.Out{Stdout: stdout, Stderr: stderr}
			for _, proj := range targetProjects {
				if !projectsync.SyncProject(out, aikitoDir, home, proj, "", false, false) {
					syncOK = false
				}
			}
			if !syncOK {
				fmt.Fprintln(stderr, "\n[ERROR] Registration completed, but synchronization was blocked.\n"+
					"Review differences with 'aikito diff' and run 'aikito sync project <project> <path> --force' to overwrite.")
				return false
			}
		}
		return true
	}

	// Global registration in skills.toml.
	skillsToml := filepath.Join(aikitoDir, "skills.toml")
	var existingGlobal []string
	original := ""
	if isRegularFile(skillsToml) {
		raw, rerr := os.ReadFile(skillsToml)
		var doc map[string]any
		if rerr == nil {
			original = string(raw)
			doc, rerr = workspace.DecodeTOML(raw)
		}
		if rerr != nil {
			return errf("Failed to read global skills configuration: %v", rerr)
		}
		if list, ok := doc["skills"].([]any); ok {
			for _, v := range list {
				existingGlobal = append(existingGlobal, pyStr(v))
			}
		}
	}
	alreadyGlobal := containsString(existingGlobal, nameClean)
	if isExistingCanonical && alreadyGlobal && !updatingImport {
		return errf("Skill '%s' is already registered globally.", nameClean)
	}
	newGlobal := existingGlobal
	if !alreadyGlobal {
		newGlobal = append(append([]string{}, existingGlobal...), nameClean)
	}
	content := sync.UpdateSkillsInToml(original, newGlobal)
	newDoc, derr := workspace.DecodeTOML([]byte(content))
	if derr != nil {
		return errf("Failed to update global skills configuration: %v", derr)
	}
	if list, ok := newDoc["skills"].([]any); !ok || !stringListEquals(list, newGlobal) {
		return errf("Failed to verify global skills configuration.")
	}

	tx, ok := newImport()
	if !ok {
		return false
	}
	lock, lerr := writerlock.Acquire(home)
	if lerr != nil {
		if tx != nil {
			tx.discard()
		}
		return errf("%v", lerr)
	}
	writeErr := func() error {
		if tx != nil {
			if err := tx.apply(); err != nil {
				return err
			}
		} else {
			if err := os.MkdirAll(skillDir, 0o777); err != nil {
				return err
			}
			if err := atomicWriteText(skillFile, skeleton); err != nil {
				return err
			}
		}
		return atomicWriteText(skillsToml, content)
	}()
	if writeErr != nil {
		if original != "" {
			_ = atomicWriteText(skillsToml, original)
		}
		if tx != nil {
			tx.rollback()
		} else {
			_ = os.RemoveAll(skillDir)
		}
		lock.Release()
		return errf("Failed to write global skill: %v", writeErr)
	}
	if tx != nil {
		tx.discard()
	}
	lock.Release()

	if updatingImport {
		fmt.Fprintf(stdout, "[UPDATE DIR] %s\n[UPDATE FILE] %s\n", disp(skillDir), disp(skillFile))
	} else {
		fmt.Fprintf(stdout, "[CREATE DIR] %s\n[CREATE FILE] %s\n", disp(skillDir), disp(skillFile))
	}
	if !alreadyGlobal {
		fmt.Fprintf(stdout, "[UPDATE FILE] %s (registered global skill)\n", disp(skillsToml))
	}
	action := "Added"
	if updatingImport {
		action = "Updated"
	}
	fmt.Fprintf(stdout, "\n[SUCCESS] %s global skill '%s'.\n", action, nameClean)
	fmt.Fprintln(stdout, "\U0001F4A1 Next steps:")
	step := 1
	if sourcePath == "" {
		fmt.Fprintf(stdout, "  %d. Update instructions in %s (or run 'aikito edit skill %s')\n", step, disp(skillFile), nameClean)
		step++
	}
	fmt.Fprintf(stdout, "  %d. Synchronize to agents: aikito sync global\n", step)

	if o.sync {
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

// planSkillRegistration adds name to a project agent.toml's skills list
// with update_skills_in_toml, checking that every other key is unchanged.
func planSkillRegistration(path, name string) (updated, original string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	original = string(raw)
	data, err := workspace.DecodeTOML(raw)
	if err != nil {
		return "", "", err
	}
	existing, _ := data["skills"].([]any)
	var newSkills []any
	seen := map[string]bool{}
	for _, v := range append(append([]any{}, existing...), name) {
		key := fmt.Sprintf("%T:%v", v, v)
		if !seen[key] {
			seen[key] = true
			newSkills = append(newSkills, v)
		}
	}
	strs := make([]string, len(newSkills))
	for i, v := range newSkills {
		strs[i] = pyStr(v)
	}
	updated = sync.UpdateSkillsInToml(original, strs)
	newData, err := workspace.DecodeTOML([]byte(updated))
	if err != nil {
		return "", "", err
	}
	for k, v := range data {
		if k != "skills" && !reflect.DeepEqual(newData[k], v) {
			return "", "", fmt.Errorf("Semantic integrity check failed for key '%s'", k)
		}
	}
	if !reflect.DeepEqual(newData["skills"], newSkills) {
		return "", "", fmt.Errorf("Semantic check failed for project '%s'", filepath.Base(filepath.Dir(path)))
	}
	return updated, original, nil
}

func containsAny(list []any, s string) bool {
	for _, v := range list {
		if str, ok := v.(string); ok && str == s {
			return true
		}
	}
	return false
}

func loadTOMLFile(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return workspace.DecodeTOML(raw)
}
