// Package cli implements the aikito command-line interface: a thin
// argument dispatcher over internal/workspace, internal/registry, and
// internal/project.
//
// Every command handler takes an explicit Environment rather than reading
// os.Getenv/os.UserHomeDir()/os.Getwd() directly — this is what lets
// commands be tested in-process against t.TempDir() with a fake
// Environment, fully parallel-safe, instead of Python's
// patch.dict(os.environ, ...)/patch.object(Path, "home", ...) approach.
// Only main.go and RealEnvironment() touch real process state.
package cli

import (
	"os"

	"github.com/mr-miles/aikito-rs/internal/workspace"
)

// Environment is the process state every command handler needs, threaded
// explicitly instead of read ambiently.
type Environment struct {
	Home string        // resolved home directory
	Env  workspace.Env // env var lookups (AIKITO_DIR, XDG_CONFIG_HOME, APPDATA, ...)
	Cwd  string        // process working directory at invocation time
}

// RealEnvironment reads actual process state. Call this exactly once, from
// main.go.
func RealEnvironment() Environment {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	return Environment{Home: home, Env: workspace.OSEnv{}, Cwd: cwd}
}

// AikitoDir mirrors cli.py's get_aikito_dir(): the resolved active
// workspace directory.
func (e Environment) AikitoDir() (string, error) {
	return workspace.ResolveWorkspace(e.Home, e.Env)
}
