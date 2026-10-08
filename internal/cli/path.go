package cli

import (
	"fmt"
	"io"
)

// cmdPath mirrors cli_parser.py's `path` subcommand group — only
// `path workspace` exists in Python today.
func cmdPath(args []string, stdout, stderr io.Writer, env Environment) int {
	if len(args) == 0 || args[0] != "workspace" {
		fmt.Fprintln(stderr, "usage: aikito path workspace")
		return 2
	}
	dir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, dir)
	return 0
}
