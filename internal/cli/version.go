package cli

import (
	"fmt"
	"io"
)

// Version is this Go port's own version string. It is intentionally
// independent of the Python package's __version__ (currently "1.57.7") —
// this is a from-scratch reimplementation, not a build of the same
// release train.
const Version = "0.1.0-dev"

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
