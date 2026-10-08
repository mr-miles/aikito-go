package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mr-miles/aikito-rs/internal/sync"
	"github.com/mr-miles/aikito-rs/internal/workspace"
)

// cmdMigrate implements `aikito migrate workspace-resources [--dry-run]`,
// the one-time, one-way conversion from a legacy pre-v2 workspace layout
// (monolithic agents.toml/subagents.toml) to the current v2 layout
// (agents/<name>.toml one-file-per-agent). See
// internal/workspace/migrate.go for the plan-building logic this wraps.
func cmdMigrate(args []string, stdout, stderr io.Writer, env Environment) int {
	if len(args) == 0 || args[0] != "workspace-resources" {
		fmt.Fprintln(stderr, "usage: aikito migrate workspace-resources [--dry-run]")
		return 2
	}
	dryRun := false
	for _, a := range args[1:] {
		switch {
		case a == "--dry-run":
			dryRun = true
		default:
			fmt.Fprintf(stderr, "[ERROR] Unknown argument: %s\n", a)
			return 2
		}
	}

	aikitoDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	// cli.py cmd_migrate_workspace_resources: on a real run, finish or roll
	// back an interrupted migration before planning. A pending "layout"
	// journal makes every other command refuse to run (see
	// workspace.RequireCurrentLayout), so this is the way out of that state.
	if !dryRun {
		recovered, err := sync.Recover([]string{aikitoDir}, migrationPathPolicy(), nil)
		if err != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", err)
			return 1
		}
		if recovered {
			fmt.Fprintln(stdout, "[RECOVER] Interrupted workspace migration recovered")
		}
	}

	plan, err := workspace.BuildMigrationPlan(aikitoDir, env.Home)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if plan.Blocked() {
		fmt.Fprintln(stderr, "[ERROR] Migration cannot proceed:")
		for _, f := range plan.Findings {
			fmt.Fprintf(stderr, "  - %s\n", f)
		}
		return 1
	}
	for _, n := range plan.Notes {
		fmt.Fprintf(stdout, "[NOTE] %s\n", n)
	}

	if len(plan.Removes) == 0 {
		fmt.Fprintln(stdout, "[OK] Workspace is already on the current layout; nothing to migrate.")
		return 0
	}

	fmt.Fprintln(stdout, "[INFO] Migration plan:")
	for _, c := range plan.Creates {
		fmt.Fprintf(stdout, "  [CREATE] %s\n", c.Path)
	}
	for _, u := range plan.Updates {
		fmt.Fprintf(stdout, "  [UPDATE] %s\n", u.Path)
	}
	for _, r := range plan.Removes {
		fmt.Fprintf(stdout, "  [REMOVE] %s\n", r)
	}
	fmt.Fprintln(stdout, "  [CREATE] layout.toml")

	if dryRun {
		fmt.Fprintln(stdout, "\n[DRY RUN] No changes written.")
		return 0
	}

	if err := applyMigration(plan, aikitoDir); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "\n[SUCCESS] Workspace migrated to the current layout.")
	return 0
}

// applyMigration mirrors apply_migration: stage every create/update/marker
// write to a temp dir, fingerprint each staged file, build the Change list
// (creates/updates/marker as After-only or Before+After, legacy file
// removals as Before-only), and commit it all through the shared
// transaction engine in one atomic batch.
func applyMigration(plan workspace.MigrationPlan, home string) error {
	policy := migrationPathPolicy()
	recovered, err := sync.Recover([]string{plan.Root}, policy, nil)
	if err != nil {
		return err
	}
	if recovered {
		return fmt.Errorf("Recovered an interrupted migration; run again")
	}
	fresh, err := workspace.BuildMigrationPlan(plan.Root, home)
	if err != nil {
		return err
	}
	if !migrationPlansEqual(fresh, plan) {
		return fmt.Errorf("workspace changed after migration planning; run migrate again")
	}

	staging, err := os.MkdirTemp("", "aikito-migrate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	var changes []sync.Change
	stage := func(relative, content, kind string) error {
		stagePath := filepath.Join(staging, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(stagePath), 0o777); err != nil {
			return err
		}
		if err := os.WriteFile(stagePath, []byte(content), 0o644); err != nil {
			return err
		}
		after, err := workspace.FingerprintResource(stagePath, kind)
		if err != nil {
			return err
		}
		var before *string
		v, verr := sync.VersionAt(plan.Root, relative, kind, policy, nil)
		if verr != nil {
			return verr
		}
		if v != nil {
			before = &v.Fingerprint
		}
		changes = append(changes, sync.Change{Target: 0, Path: relative, Kind: kind, Source: stagePath, Before: before, After: &after})
		return nil
	}

	for _, c := range plan.Creates {
		if err := stage(c.Path, c.Content, "agent"); err != nil {
			return err
		}
	}
	for _, u := range plan.Updates {
		if err := stage(u.Path, u.Content, "subagent"); err != nil {
			return err
		}
	}

	for _, relative := range plan.Removes {
		v, err := sync.VersionAt(plan.Root, relative, "legacy", policy, nil)
		if err != nil {
			return err
		}
		if v == nil {
			return fmt.Errorf("legacy file vanished during migration: %s", relative)
		}
		changes = append(changes, sync.Change{Target: 0, Path: relative, Kind: "legacy", Before: &v.Fingerprint, After: nil})
	}

	if err := stage("layout.toml", plan.MarkerContent, "layout"); err != nil {
		return err
	}

	verify := func() error {
		if _, err := workspace.ReadAgentDocuments(plan.Root); err != nil {
			return err
		}
		subDir := filepath.Join(plan.Root, "subagents")
		entries, err := os.ReadDir(subDir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			n := e.Name()
			if n == ".DS_Store" || n == "Thumbs.db" || n == "desktop.ini" {
				continue
			}
			stem := strings.TrimSuffix(n, filepath.Ext(n))
			if !strings.HasSuffix(n, ".md") || workspace.ValidateResourceName(stem, "subagent") != "" {
				return fmt.Errorf("unsupported subagent entry: %s", filepath.Join(subDir, n))
			}
			if _, _, err := workspace.ParseSubagentFile(filepath.Join(subDir, n)); err != nil {
				return err
			}
		}
		for _, name := range workspace.LegacyFiles {
			if _, err := os.Lstat(filepath.Join(plan.Root, name)); err == nil {
				return fmt.Errorf("legacy files remain after migration")
			}
		}
		return nil
	}

	return sync.Apply([]string{plan.Root}, changes, nil, verify, policy, nil)
}

// migrationPathPolicy mirrors layout.py migration_path_policy. The journal
// records the policy it was written under and Recover requires the caller's
// Resources/States to match it, so applyMigration and the pre-plan Recover
// must share this one definition.
func migrationPathPolicy() sync.PathPolicy {
	return sync.PathPolicy{
		Resources: [][2]string{
			{"legacy", "agents.toml"},
			{"legacy", "subagents.toml"},
			{"layout", "layout.toml"},
		},
		CreateParents: true,
	}
}

func migrationPlansEqual(a, b workspace.MigrationPlan) bool {
	return fragmentsEqual(a.Creates, b.Creates) &&
		fragmentsEqual(a.Updates, b.Updates) &&
		stringsEqualOrdered(a.Removes, b.Removes) &&
		stringsEqualOrdered(a.Findings, b.Findings) &&
		a.MarkerContent == b.MarkerContent
}

func fragmentsEqual(a, b []workspace.FileFragment) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stringsEqualOrdered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
