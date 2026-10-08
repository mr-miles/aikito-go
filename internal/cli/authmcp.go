package cli

import (
	"fmt"
	"io"

	"github.com/mr-miles/aikito-rs/internal/mcp"
)

// cmdAuthMCP implements `aikito auth mcp <agent> <server>`. This is a thin
// wrapper around the already-built mcp.AuthenticateMCP (cli.py's
// cmd_mcp_auth is equally thin: positional agent/server, no flags, print
// any MCPConfigError to stderr and exit 1, else exit 1 iff the call
// reports failure).
func cmdAuth(args []string, stdout, stderr io.Writer, env Environment) int {
	if len(args) == 0 || args[0] != "mcp" {
		fmt.Fprintln(stderr, "[ERROR] Usage: aikito auth mcp <agent> <server>")
		return 2
	}
	rest := args[1:]
	if len(rest) != 2 {
		fmt.Fprintln(stderr, "[ERROR] Usage: aikito auth mcp <agent> <server>")
		return 2
	}
	agent, server := rest[0], rest[1]

	aikitoDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if msg := checkWorkspaceInitialized(aikitoDir); msg != "" {
		fmt.Fprintf(stderr, "[ERROR] %s\n", msg)
		return 1
	}

	success, err := mcp.AuthenticateMCP(aikitoDir, env.Home, agent, server, func(line string) {
		fmt.Fprintln(stdout, line)
	}, true)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if !success {
		return 1
	}
	return 0
}
