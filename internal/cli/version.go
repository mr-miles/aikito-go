package cli

import (
	"fmt"
	"io"
	"regexp"
	"runtime/debug"
	"strings"
)

// Version is this Go port's own version string, independent of the Python
// package's __version__. Release builds stamp it with
// -ldflags "-X github.com/mr-miles/aikito-rs/internal/cli.Version=..."
// (see .goreleaser.yaml); a plain `go build` keeps the default.
var Version = "0.1.0-dev"

func init() {
	// `go install github.com/mr-miles/aikito-rs/cmd/aikito@v0.1.0` doesn't
	// apply ldflags, but Go records the module version in the binary.
	if Version != "0.1.0-dev" {
		return
	}
	// Local builds in a git checkout also record a version, but it's a
	// pseudo-version (v0.0.0-20261008143133-938c71b01fb6+dirty); only a
	// real tagged version replaces the default.
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; isReleaseVersion(v) {
			Version = strings.TrimPrefix(v, "v")
		}
	}
}

var pseudoVersionSuffix = regexp.MustCompile(`\d{14}-[0-9a-f]{12}$`)

func isReleaseVersion(v string) bool {
	return strings.HasPrefix(v, "v") && !strings.Contains(v, "+") && !pseudoVersionSuffix.MatchString(v)
}

// cmdVersion mirrors update_notifier.py's cmd_version, minus the PyPI
// update-check network call: that's an out-of-scope phase-1 feature (no
// network I/O belongs in a pure command handler's default path). -c/--force
// are accepted but report that update checking isn't implemented in this
// build, rather than silently doing nothing or failing.
func cmdVersion(args []string, stdout, stderr io.Writer) int {
	jsonOutput := false
	checkRequested := false
	for _, a := range args {
		switch a {
		case "--json":
			jsonOutput = true
		case "-c", "--check", "--force":
			checkRequested = true
		}
	}

	if jsonOutput {
		fmt.Fprintf(stdout, "{\n  \"version\": %q,\n  \"latest_version\": null,\n  \"update_available\": false,\n  \"upgrade_command\": null\n}\n", Version)
		return 0
	}

	fmt.Fprintf(stdout, "aikito %s\n", Version)
	if checkRequested {
		fmt.Fprintln(stderr, "[WARNING] Update checking is not implemented in this build.")
	}
	return 0
}
