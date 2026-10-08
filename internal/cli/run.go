package cli

import (
	"fmt"
	"io"
)

// Run is the pure CLI entrypoint: parses args, dispatches to a command
// handler, and returns a process exit code. It performs no I/O against real
// process state beyond what's passed in via stdin/stdout/stderr/env.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, env Environment) int {
	// --debug only switches Python's traceback printing; Go has none to show.
	for len(args) > 0 && args[0] == "--debug" {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, noArgsUsage())
		return 2
	}
	if handleHelp(args, stdout) {
		return 0
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
	case "show":
		return cmdShow(args[1:], stdout, stderr, env)
	case "sync":
		return cmdSync(args[1:], stdout, stderr, env)
	case "status":
		return cmdStatus(args[1:], stdout, stderr, env)
	case "edit":
		return cmdEdit(args[1:], stdout, stderr, env)
	case "rm", "remove":
		return cmdRm(args[0], args[1:], stdout, stderr, env)
	case "diff":
		return cmdDiff(args[1:], stdout, stderr, env)
	case "auth":
		return cmdAuth(args[1:], stdout, stderr, env)
	case "rename":
		return cmdRename(args[1:], stdout, stderr, env)
	case "maintain":
		return cmdMaintain(args[1:], stdout, stderr, env)
	case "completion":
		return cmdCompletion(args[1:], stdout, stderr, env)
	case "doctor":
		return cmdDoctor(args[1:], stdout, stderr, env)
	case "import":
		return cmdImport(args[1:], stdout, stderr, env)
	case "migrate":
		return cmdMigrate(args[1:], stdout, stderr, env)
	default:
		fmt.Fprintf(stderr, "[ERROR] Unknown command: %s\n", args[0])
		return 2
	}
}
