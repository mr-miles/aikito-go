package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Env abstracts process environment lookups so workspace path resolution is
// testable without mutating real process state (os.Setenv), matching the
// Go port's "explicit environment struct" testing strategy rather than
// Python's patch.dict(os.environ, ...).
type Env interface {
	Getenv(key string) string
}

// OSEnv reads the real process environment.
type OSEnv struct{}

func (OSEnv) Getenv(key string) string { return os.Getenv(key) }

// MapEnv is a fake Env backed by a plain map, for tests.
type MapEnv map[string]string

func (m MapEnv) Getenv(key string) string { return m[key] }

// WorkspaceConfigDir mirrors compat.py's get_workspace_config_dir: the
// configuration base directory (XDG_CONFIG_HOME, or %APPDATA% on Windows,
// falling back to home/.config) plus "aikito".
func WorkspaceConfigDir(home string, env Env) string {
	if runtime.GOOS == "windows" {
		if appData := env.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "aikito")
		}
		return filepath.Join(home, ".config", "aikito")
	}
	if configHome := env.Getenv("XDG_CONFIG_HOME"); configHome != "" {
		return filepath.Join(ExpandUser(home, configHome), "aikito")
	}
	return filepath.Join(home, ".config", "aikito")
}

// WorkspacePointerPath mirrors get_workspace_pointer_path: the user-level
// file that stores the persisted default workspace path.
func WorkspacePointerPath(home string, env Env) string {
	return filepath.Join(WorkspaceConfigDir(home, env), "workspace")
}

// ExpandUser expands a leading "~" or "~/..." (and "~\..." on Windows) using
// home, mirroring pathlib.Path.expanduser() for the subset of forms this
// codebase actually produces (it never authors "~otheruser/...").
func ExpandUser(home, path string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	if runtime.GOOS == "windows" && strings.HasPrefix(path, `~\`) {
		return filepath.Join(home, path[2:])
	}
	return path
}

// ResolvePath approximates pathlib.Path.resolve(strict=False): make the
// path absolute (relative to the process's current working directory, not
// to home — Python's Path.resolve() behaves the same way), clean it, and
// resolve symlinks for as much of the path as exists on disk, leaving any
// non-existent trailing components untouched.
func ResolvePath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)

	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved, nil
	}

	var trailing []string
	cur := path
	for {
		if _, err := os.Lstat(cur); err == nil {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return path, nil // nothing on this path exists at all
		}
		trailing = append([]string{filepath.Base(cur)}, trailing...)
		cur = parent
	}
	resolvedBase, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return path, nil
	}
	return filepath.Join(append([]string{resolvedBase}, trailing...)...), nil
}

// ResolveWorkspaceWithSource mirrors workspace/paths.py
// resolve_workspace_with_source: $AIKITO_DIR env var, else the persisted
// pointer file under the workspace config dir, else home/aikito. The
// returned source is "AIKITO_DIR", "configured", or "default", preserved
// for diagnostics (e.g. `aikito path workspace`).
func ResolveWorkspaceWithSource(home string, env Env) (path string, source string, err error) {
	if envDir := env.Getenv("AIKITO_DIR"); envDir != "" {
		resolved, rerr := ResolvePath(ExpandUser(home, envDir))
		if rerr != nil {
			return "", "", rerr
		}
		return resolved, "AIKITO_DIR", nil
	}

	pointerPath := WorkspacePointerPath(home, env)
	configuredDir := ""
	if data, rerr := os.ReadFile(pointerPath); rerr == nil {
		configuredDir = strings.TrimSpace(string(data))
	}
	if configuredDir != "" {
		resolved, rerr := ResolvePath(ExpandUser(home, configuredDir))
		if rerr != nil {
			return "", "", rerr
		}
		return resolved, "configured", nil
	}

	resolved, rerr := ResolvePath(filepath.Join(home, "aikito"))
	if rerr != nil {
		return "", "", rerr
	}
	return resolved, "default", nil
}

// ResolveWorkspace is ResolveWorkspaceWithSource without the source tag.
func ResolveWorkspace(home string, env Env) (string, error) {
	path, _, err := ResolveWorkspaceWithSource(home, env)
	return path, err
}

// PersistWorkspace mirrors persist_workspace: writes the resolved workspace
// path (plus trailing newline) to the pointer file, creating parent
// directories as needed. This is the only legitimate writer of the pointer
// file; it is invoked by `aikito init workspace <path>`.
func PersistWorkspace(workspace, home string, env Env) (string, error) {
	resolved, err := ResolvePath(ExpandUser(home, workspace))
	if err != nil {
		return "", err
	}
	pointerPath := WorkspacePointerPath(home, env)
	if err := os.MkdirAll(filepath.Dir(pointerPath), 0o777); err != nil {
		return "", err
	}
	if err := os.WriteFile(pointerPath, []byte(resolved+"\n"), 0o644); err != nil {
		return "", err
	}
	return pointerPath, nil
}
