// Command aikito is the Go port's CLI binary. It is a thin wrapper: all
// actual command logic lives in internal/cli as a pure function of
// (args, stdio, Environment), so it can be exercised in-process by tests
// without spawning a subprocess.
package main

import (
	"os"

	"github.com/mr-miles/aikito-rs/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, cli.RealEnvironment()))
}
