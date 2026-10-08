// Package sync implements the cross-domain plan-observation protocol and the
// shared atomic filesystem transaction engine that every sync/adopt/import
// domain plan ultimately bottoms out through.
package sync

// Outcome is the result of a three-way comparison of one logical resource,
// mirroring workspace/merge.py's Outcome.
type Outcome struct {
	Action string // "CREATE", "UPDATE", "DELETE", "NOOP", or "CONFLICT"
	Target string // "local" or "remote" for changes, "" otherwise
	Reason string
}

// Compare decides one resource's outcome; base holds every fingerprint
// counted as "unmodified" (the common ancestor set — plural, since a
// resource id's unmodified status can match more than one historical
// fingerprint, e.g. several template versions). An empty base means the
// resource had no common ancestor at all, so only a missing side (nil)
// counts as unmodified; nil marks a missing side on either local or remote.
//
// This is a pure function over fingerprints with no filesystem access and
// no opinion about what base/local/remote mean to the caller: import calls
// it with a bundled-template fingerprint set as base, and a future
// reconcile domain would call it with a replica's last-agreed snapshot as
// base — same function, different ancestor sets.
func Compare(base map[string]struct{}, local, remote *string) Outcome {
	if ptrEqual(local, remote) {
		return Outcome{"NOOP", "", "Both sides agree"}
	}
	localSame := inBaseOrMissing(base, local)
	remoteSame := inBaseOrMissing(base, remote)
	switch {
	case localSame && remoteSame:
		return Outcome{"NOOP", "", "Both sides are unmodified"}
	case localSame:
		return Outcome{action(local, remote), "local", "Remote changed"}
	case remoteSame:
		return Outcome{action(remote, local), "remote", "Local changed"}
	default:
		return Outcome{"CONFLICT", "", "Both sides changed"}
	}
}

func inBaseOrMissing(base map[string]struct{}, side *string) bool {
	if side == nil {
		return len(base) == 0
	}
	_, ok := base[*side]
	return ok
}

func action(current, next *string) string {
	if next == nil {
		return "DELETE"
	}
	if current == nil {
		return "CREATE"
	}
	return "UPDATE"
}

func ptrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// BaseSet builds a base fingerprint set from a list of fingerprints, a
// convenience for callers that have a slice rather than a map literal.
func BaseSet(fingerprints ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(fingerprints))
	for _, fp := range fingerprints {
		set[fp] = struct{}{}
	}
	return set
}
