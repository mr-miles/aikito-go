package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// cmdGit mirrors cli.py's cmd_git: forwards all given args straight to a
// real `git -C <workspace> ...` subprocess, failing with a clear message if
// the workspace doesn't exist, isn't a Git repo, or git isn't on PATH.
func cmdGit(args []string, stdin io.Reader, stdout, stderr io.Writer, env Environment) int {
	workspaceDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if _, err := os.Stat(workspaceDir); err != nil {
		fmt.Fprintf(stderr, "[ERROR] Workspace does not exist: %s. Run 'aikito init workspace' first.\n", workspaceDir)
		return 1
	}
	if _, err := os.Stat(filepath.Join(workspaceDir, ".git")); err != nil {
		fmt.Fprintf(stderr, "[ERROR] Workspace '%s' is not a Git repository. Run 'aikito init workspace' first.\n", workspaceDir)
		return 1
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		fmt.Fprintln(stderr, "[ERROR] 'git' executable not found in PATH.")
		return 1
	}

	gitArgs := append([]string(nil), args...)
	if len(gitArgs) > 0 && gitArgs[0] == "--" {
		gitArgs = gitArgs[1:]
	}

	cmdArgs := append([]string{"-C", workspaceDir}, gitArgs...)
	cmd := exec.Command(gitBin, cmdArgs...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(stderr, "[ERROR] Failed to run git: %v\n", err)
		return 1
	}
	return 0
}
