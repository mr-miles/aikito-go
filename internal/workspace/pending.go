package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// transactionStateParts is the per-root directory chain holding the
// transaction journal (transactions.py _state_dir). internal/sync owns
// writing it; this package only reads it.
var transactionStateParts = []string{".local", "state", "aikito", "workspace-transactions"}

// PendingTransactionKinds mirrors transactions.py pending_kinds: the resource
// kinds named in an unfinished transaction journal under any of roots,
// without writing anything. It lives here rather than in internal/sync
// because RequireCurrentLayout needs it and internal/sync imports this
// package; sync.PendingKinds delegates to it.
func PendingTransactionKinds(roots []string) (map[string]struct{}, error) {
	kinds := map[string]struct{}{}
	for _, root := range roots {
		path, ok, err := pendingJournalPath(root)
		if err != nil {
			return nil, err
		}
		if !ok || classifyEntry(path) == entryMissing {
			continue
		}
		if classifyEntry(path) != entryFile {
			return nil, coreErrorf("Unsafe journal: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, coreErrorf("Invalid workspace journal: %s", path)
		}
		// Python indexes data["changes"] and item["kind"] directly, so a
		// missing key is an invalid journal, not an empty one.
		var raw struct {
			Changes *[]struct {
				Kind *string `json:"kind"`
			} `json:"changes"`
		}
		if err := json.Unmarshal(data, &raw); err != nil || raw.Changes == nil {
			return nil, coreErrorf("Invalid workspace journal: %s", path)
		}
		for _, c := range *raw.Changes {
			if c.Kind == nil {
				return nil, coreErrorf("Invalid workspace journal: %s", path)
			}
			kinds[*c.Kind] = struct{}{}
		}
	}
	return kinds, nil
}

// pendingJournalPath is the read-only form of transactions.py
// _journal_path(root, create=False): a missing component means no journal;
// an existing component that isn't a plain directory (including a symlink)
// is unsafe.
func pendingJournalPath(root string) (string, bool, error) {
	current := root
	for _, part := range transactionStateParts {
		current = filepath.Join(current, part)
		switch classifyEntry(current) {
		case entryMissing:
			return "", false, nil
		case entryDirectory:
		default:
			return "", false, coreErrorf("Unsafe directory: %s", current)
		}
	}
	return filepath.Join(current, "pending.json"), true, nil
}
