// Package faultinject is a test seam for crash-recovery tests: binaries
// built with the aikito_faultinject tag can stop the process at named
// points to leave a transaction half-done, as a crash or kill would.
// Release builds don't include the tag, so Point is a no-op there.
package faultinject

// hook is set only by the aikito_faultinject build (see enabled.go).
var hook func(point string)

// Point marks a place a test may stop the process. Points:
//
//	skill-journal     after a project-skill transaction journal is written
//	                  (Python: skill_runtime.write_transaction_journal)
//	workspace-rename  after each rename in the workspace transaction engine
//	                  (Python: os.replace in workspace/transactions.py)
func Point(point string) {
	if hook != nil {
		hook(point)
	}
}
