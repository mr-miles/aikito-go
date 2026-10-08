package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/mr-miles/aikito-go/internal/compat"
	"github.com/mr-miles/aikito-go/internal/project"
	"github.com/mr-miles/aikito-go/internal/projectsync"
	"github.com/mr-miles/aikito-go/internal/workspace"
)

// cmdSyncProject implements `aikito sync project [--dry-run] [--force]
// [project_name] [project_path]`, a port of cli.py's cmd_project_sync on
// top of internal/projectsync.
func cmdSyncProject(args []string, stdout, stderr io.Writer, env Environment) int {
	parsed, ok := parseArgparse("sync project", args, []string{"--dry-run", "--force"}, 2, stderr)
	if !ok {
		return 2
	}
	var rawNames, projectPath string
	if len(parsed.positionals) > 0 {
		rawNames = parsed.positionals[0]
	}
	if len(parsed.positionals) > 1 {
		projectPath = parsed.positionals[1]
	}
	return runProjectSync(rawNames, projectPath, parsed.flags["--dry-run"], parsed.flags["--force"], stdout, stderr, env)
}

// runProjectSync is the body of cmd_project_sync, shared with init project.
func runProjectSync(rawNames, projectPath string, dryRun, force bool, stdout, stderr io.Writer, env Environment) int {
	aikitoDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	var names []string
	if rawNames == "" || rawNames == "." {
		detected, err := project.DetectCurrentProject(aikitoDir, env.Cwd, env.Home)
		var conflict *project.ContextConflictError
		if errors.As(err, &conflict) {
			fmt.Fprintf(stderr, "[CONFLICT] Multiple projects match current directory '%s': %s\n", conflict.Path, strings.Join(conflict.Projects, ", "))
			return 1
		}
		if detected == "" {
			cwd, rerr := workspace.ResolvePath(env.Cwd)
			if rerr != nil {
				cwd = env.Cwd
			}
			fmt.Fprintf(stderr, "[ERROR] Current directory is not inside a registered project: %s\n", cwd)
			if rawNames == "" {
				fmt.Fprintln(stderr, "Please specify a project name, e.g. 'aikito sync project <name>'")
			}
			return 1
		}
		if rawNames == "" {
			fmt.Fprintf(stdout, "[aikito] Target project: '%s' (detected from cwd)\n", detected)
		}
		names = []string{detected}
	} else {
		for _, p := range strings.Split(rawNames, ",") {
			if p = strings.TrimSpace(p); p != "" {
				names = append(names, p)
			}
		}
	}

	if len(names) > 1 && projectPath != "" {
		fmt.Fprintln(stderr, "[ERROR] Cannot specify explicit project_path when syncing multiple projects.")
		return 1
	}

	out := projectsync.Out{Stdout: stdout, Stderr: stderr}
	for _, name := range names {
		if !compat.CanSymlink() {
			fmt.Fprintln(stderr, "[ERROR] This platform does not support symbolic links, which Aikito requires.")
			return 1
		}
		path := ""
		if len(names) == 1 {
			path = projectPath
		}
		if path != "" {
			path = resolveAgainstCwd(env, path)
		}
		if !projectsync.SyncProject(out, aikitoDir, env.Home, name, path, dryRun, force) {
			return 1
		}
	}
	return 0
}

// resolveAgainstCwd makes a relative path argument relative to the
// command's working directory, as Path(arg).resolve() does in Python.
func resolveAgainstCwd(env Environment, p string) string {
	p = workspace.ExpandUser(env.Home, p)
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(env.Cwd, p)
}
