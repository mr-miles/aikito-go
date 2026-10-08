// `aikito import workspace <source>`: merges a SECOND Aikito workspace's
// canonical resources into the current (target) workspace — e.g. migrating
// an old machine's workspace export, or merging two teammates' workspaces.
// This is distinct from `adopt` (imports agent-NATIVE config into the
// canonical workspace) and `sync` (pushes the canonical workspace OUT to
// agent-native configs).
//
// Ported from workspace/importing.py + workspace/import_decisions.py
// (430 lines total — small enough to port in full, unlike `sync project`'s
// ~3,300-line dependency chain).
//
// Deliberate simplifications, documented rather than hidden:
//   - decide_resource's real "base" ancestor is templates.py's
//     TEMPLATE_HISTORY (bundled-template fingerprint history), which no
//     part of this Go port implements yet (the same gap adopt.go already
//     documents). Without it this import always uses an EMPTY base set,
//     which per merge.Compare's semantics means a resource present on both
//     sides with differing content is always CONFLICT "Contents differ" —
//     it can never recognize "target still has the unmodified bundled
//     template, safe to update" the way Python's real adopt/import can.
//   - Python's build_import_plan proactively downgrades an item to CONFLICT
//     at PLAN time if applying it would leave a dangling reference
//     (reference_conflicts), so a dry-run preview shows the problem before
//     any write is attempted. This Go port instead relies on
//     sync.ApplyResourceWrites' own built-in MissingReferences check, which
//     only runs at apply time — a --dry-run preview here may show an item
//     as CREATE/UPDATE that a real apply would actually reject. Each
//     behaves safely (no write happens either way), just with a less
//     informative dry-run in this reduced version.
//   - Inbox-path reconfiguration (importing a source whose configured
//     inbox.path differs from the target's, including the "would orphan
//     existing target notes" BLOCKED case) is not ported: imported inbox
//     notes always land under the TARGET's current inbox prefix, and the
//     config:inbox.path resource itself is simply skipped (never
//     imported), matching the architecture's documented LOCAL_CONFIG rule
//     (inbox location is per-machine) rather than Python's richer
//     reconciliation.
//   - No separate human-facing backup copy: writes go through the same
//     crash-safe transaction engine every other command uses, not an
//     additional backup-before-write step.
package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-rs/internal/sync"
	"github.com/mr-miles/aikito-rs/internal/workspace"
)

func cmdImport(args []string, stdout, stderr io.Writer, env Environment) int {
	if len(args) == 0 || args[0] != "workspace" {
		fmt.Fprintln(stderr, "usage: aikito import workspace <source> [--dry-run] [--verbose] [--keep-target RESOURCE]* [--take-source RESOURCE]*")
		return 2
	}
	args = args[1:]

	var source string
	dryRun := false
	verbose := false
	var keepTarget, takeSource []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dry-run":
			dryRun = true
		case a == "--verbose":
			verbose = true
		case a == "--keep-target":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "[ERROR] --keep-target requires a value")
				return 2
			}
			keepTarget = append(keepTarget, args[i])
		case a == "--take-source":
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "[ERROR] --take-source requires a value")
				return 2
			}
			takeSource = append(takeSource, args[i])
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(stderr, "[ERROR] Unknown flag: %s\n", a)
			return 2
		default:
			if source == "" {
				source = a
			}
		}
	}
	if source == "" {
		fmt.Fprintln(stderr, "[ERROR] Usage: aikito import workspace <source> ...")
		return 2
	}

	keepSet := map[string]bool{}
	for _, k := range keepTarget {
		keepSet[k] = true
	}
	for _, k := range takeSource {
		if keepSet[k] {
			fmt.Fprintf(stderr, "[ERROR] Conflicting resolutions for: %s\n", k)
			return 1
		}
	}
	takeSet := map[string]bool{}
	for _, k := range takeSource {
		takeSet[k] = true
	}

	sourceRoot, err := workspace.ResolvePath(workspace.ExpandUser(env.Home, source))
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	targetRoot, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if err := workspace.RequireCurrentLayout(sourceRoot); err != nil {
		fmt.Fprintf(stderr, "[ERROR] Source is not an Aikito workspace: %v\n", err)
		return 1
	}
	if err := workspace.RequireCurrentLayout(targetRoot); err != nil {
		fmt.Fprintf(stderr, "[ERROR] Target is not an Aikito workspace: %v\n", err)
		return 1
	}

	sourceSnapshot, err := workspace.SnapshotWorkspace(sourceRoot, env.Home)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] Source: %v\n", err)
		return 1
	}
	targetSnapshot, err := workspace.SnapshotWorkspace(targetRoot, env.Home)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] Target: %v\n", err)
		return 1
	}

	// importing.py: every --keep-target/--take-source ID must name a resource
	// in the source workspace (its _SUPPORTED set is every resource kind).
	for _, ids := range [][]string{keepTarget, takeSource} {
		for _, id := range ids {
			if _, ok := sourceSnapshot.Resources[id]; !ok {
				fmt.Fprintf(stderr, "[ERROR] Unknown import resource ID: %s\n", id)
				return 1
			}
		}
	}

	type planItem struct {
		id, kind, name, relPath, reason, action string
		before                                  *string
		fingerprint                             string
	}
	var items []planItem
	for id, resource := range sourceSnapshot.Resources {
		if resource.Kind == "config" && resource.Name == "inbox.path" {
			continue // host-local; never imported, see package doc.
		}
		target, hasTarget := targetSnapshot.Resources[id]

		action, reason := "CREATE", "Resource is absent from target"
		var before *string
		if hasTarget {
			before = &target.Fingerprint
			if target.Fingerprint == resource.Fingerprint {
				action, reason = "NOOP", "Contents match"
			} else {
				outcome := sync.Compare(map[string]struct{}{}, before, &resource.Fingerprint)
				switch {
				case outcome.Action == "NOOP":
					action, reason = "NOOP", "Both sides are unmodified"
				case outcome.Target == "local":
					action, reason = "UPDATE", "Replace unmodified content with source"
				case outcome.Target == "remote":
					action, reason = "NOOP", "Target is customized; source matches"
				default:
					action, reason = "CONFLICT", "Contents differ"
				}
			}
		}

		if keepSet[id] {
			action, reason = "NOOP", "Skipped by --keep-target; target kept unchanged"
		} else if action == "CONFLICT" && takeSet[id] {
			action, reason = "UPDATE", "Conflict resolved using source"
		}

		relPath := resource.Parts[0].Path
		items = append(items, planItem{
			id: id, kind: resource.Kind, name: resource.Name, relPath: relPath,
			reason: reason, action: action, before: before, fingerprint: resource.Fingerprint,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].id < items[j].id })

	counts := map[string]int{}
	var writes []sync.ResourceWrite
	for _, it := range items {
		counts[it.action]++
		if it.action == "CREATE" || it.action == "UPDATE" {
			fp := it.fingerprint
			writes = append(writes, sync.ResourceWrite{
				RelativePath: it.relPath, Kind: it.kind, Name: it.name,
				Fingerprint: &fp, Before: it.before,
			})
		}
	}

	for _, it := range items {
		switch {
		case it.action == "NOOP":
			if verbose {
				fmt.Fprintf(stdout, "[NOOP] %s (%s)\n", it.relPath, it.id)
			}
		case it.action == "CONFLICT":
			fmt.Fprintf(stdout, "[CONFLICT] %s (%s): %s\n", it.relPath, it.id, it.reason)
		default:
			detail := ""
			if verbose {
				detail = fmt.Sprintf(" (%s)", it.id)
			}
			fmt.Fprintf(stdout, "[%s] %s%s\n", it.action, it.relPath, detail)
		}
	}

	for _, f := range workspace.ScanCredentials(sourceSnapshot) {
		fmt.Fprintf(stderr, "[WARNING] %s: %s\n", f.Resource, f.Message)
	}

	fmt.Fprintf(stdout, "\nSummary: %d create, %d update, %d conflict, %d unchanged\n",
		counts["CREATE"], counts["UPDATE"], counts["CONFLICT"], counts["NOOP"])

	// Like cmd_import_workspace: conflicts never block the non-conflicting
	// resources — those are still imported (or previewed), conflicting ones
	// are left unchanged, and the command exits 2 so the caller knows
	// resolution is still needed.
	conflicts := counts["CONFLICT"] > 0
	switch {
	case dryRun:
		fmt.Fprintln(stdout, "\n[DRY RUN] No changes written.")
	case len(writes) > 0:
		policy := sync.PathPolicy{CreateParents: true}
		if err := sync.ApplyResourceWrites(sourceSnapshot, targetSnapshot, writes, policy, nil, env.Home, nil); err != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", err)
			return 1
		}
		if conflicts {
			fmt.Fprintf(stdout, "\n[PARTIAL] Imported %d non-conflicting resource(s) from %s; conflicts kept unchanged.\n", len(writes), sourceRoot)
		} else {
			fmt.Fprintf(stdout, "\n[SUCCESS] Imported %d resource(s) from %s.\n", len(writes), sourceRoot)
		}
	case conflicts:
		fmt.Fprintln(stdout, "\n[PARTIAL] No resources changed; conflicts kept unchanged.")
	default:
		fmt.Fprintln(stdout, "\n[SUCCESS] Nothing to import; target already has everything.")
	}
	if conflicts {
		fmt.Fprintln(stderr, "[NEXT] Resolve conflicts with --keep-target RESOURCE_ID or --take-source RESOURCE_ID, or edit the resources and retry.")
		return 2
	}
	return 0
}
