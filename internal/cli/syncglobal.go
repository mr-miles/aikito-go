package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mr-miles/aikito-go/internal/compat"
	"github.com/mr-miles/aikito-go/internal/workspace"
)

// symlinkRequirementGuidance is compat.py's SYMLINK_REQUIREMENT_GUIDANCE.
const symlinkRequirementGuidance = "[ERROR] Aikito requires symbolic link support to manage Agent resources.\n" +
	"On Windows, symbolic links require enabling Developer Mode (no restart required):\n\n" +
	"  Option 1 (Windows Settings GUI):\n" +
	"    Settings -> System -> For developers -> Developer Mode -> Turn On\n\n" +
	"  Option 2 (PowerShell / Command Prompt as Administrator):\n" +
	`    reg add "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\AppModelUnlock" /t REG_DWORD /f /v "AllowDevelopmentWithoutDevLicense" /d "1"` + "\n"

// argparseUnrecognized prints argparse's top-level "unrecognized arguments"
// error, which is what the reference CLI emits for any extra argument to a
// subcommand.
func argparseUnrecognized(stderr io.Writer, extra []string) int {
	usage := helpTexts["__noargs__"]
	if i := strings.Index(usage, "aikito: error:"); i >= 0 {
		usage = usage[:i]
	}
	fmt.Fprintf(stderr, "%saikito: error: unrecognized arguments: %s\n", usage, strings.Join(extra, " "))
	return 2
}

// requireLayoutLikePython is cli.py main()'s gate: only a directory that
// already looks like a workspace has to be on the current layout.
func requireLayoutLikePython(aikitoDir string) error {
	known := isRecognizedWorkspace(aikitoDir)
	for _, name := range []string{"agents.toml", "subagents.toml", "layout.toml"} {
		if _, err := os.Lstat(filepath.Join(aikitoDir, name)); err == nil {
			known = true
		}
	}
	if !known {
		return nil
	}
	return workspace.RequireCurrentLayout(aikitoDir)
}

// safeRelativePath is compat.py's safe_relative_path: "~/rel" under base,
// else the path as-is.
func safeRelativePath(path, base string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(path)
	}
	return "~/" + filepath.ToSlash(rel)
}

// collectResourceConflicts ports conflict.py's collect_resource_conflicts.
func collectResourceConflicts(paths []string, home string) []string {
	var errs []string
	seen := map[string]bool{}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		var candidates []string
		switch {
		case info.Mode().IsRegular():
			candidates = []string{p}
		case info.IsDir():
			_ = filepath.WalkDir(p, func(fp string, d os.DirEntry, err error) error {
				if err == nil && d.Type().IsRegular() {
					candidates = append(candidates, fp)
				}
				return nil
			})
		default:
			continue
		}
		for _, fp := range candidates {
			resolved, _ := compat.ResolvePath(fp)
			if seen[resolved] {
				continue
			}
			seen[resolved] = true
			blocking, _ := findConflictMarkerLines(fp)
			for _, ln := range blocking {
				errs = append(errs, fmt.Sprintf("%s:%d: Git conflict marker detected", safeRelativePath(fp, home), ln))
			}
		}
	}
	return errs
}

// cmdSyncGlobal ports cli.py's cmd_global_sync / sync_global_resources and
// workspace/sync.py's build_global_sync_plan + execute_global_sync_plan:
// the shared ~/.agents/skills container with one link per selected skill,
// one consumer link per distinct agent skills path (agents already reading
// ~/.agents/skills share it), and one link per distinct instruction path.
func cmdSyncGlobal(args []string, stdout, stderr io.Writer, env Environment) int {
	dryRun := false
	var extra []string
	for _, a := range args {
		if a == "--dry-run" {
			dryRun = true
		} else {
			extra = append(extra, a)
		}
	}
	if len(extra) > 0 {
		return argparseUnrecognized(stderr, extra)
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
	if !compat.CanSymlink() {
		fmt.Fprint(stderr, symlinkRequirementGuidance)
		return 1
	}
	if syncGlobalResources(aikitoDir, env.Home, dryRun, stdout, stderr) {
		return 0
	}
	return 1
}

func isRegularFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

func or(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}
