package cli

import (
	"fmt"
	"io"
)

// Run is the pure CLI entrypoint: parses args, dispatches to a command
// handler, and returns a process exit code. It performs no I/O against real
// process state beyond what's passed in via stdin/stdout/stderr/env.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, env Environment) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: aikito <command> [args...]")
		return 2
	}

	switch args[0] {
	case "-v", "--version":
		return cmdVersion(nil, stdout, stderr)
	case "version":
		return cmdVersion(args[1:], stdout, stderr)
	case "path":
		return cmdPath(args[1:], stdout, stderr, env)
	case "git":
		return cmdGit(args[1:], stdin, stdout, stderr, env)
	case "init":
		return cmdInit(args[1:], stdout, stderr, env)
	case "add":
		return cmdAdd(args[1:], stdout, stderr, env)
	case "adopt":
		return cmdAdopt(args[1:], stdout, stderr, env)
	default:
		fmt.Fprintf(stderr, "[ERROR] Unknown command: %s\n", args[0])
		return 2
	}
}
