package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mr-miles/aikito-go/internal/sync"
	"github.com/mr-miles/aikito-go/internal/workspace"
	"github.com/mr-miles/aikito-go/internal/writerlock"
)

// cmdMigrate implements `aikito migrate workspace-resources [--dry-run]`
// (cli.py cmd_migrate_workspace_resources), the one-time, one-way
// conversion from a legacy pre-v2 workspace layout (monolithic
// agents.toml/subagents.toml) to the current layout (agents/<name>.toml).
// See internal/workspace/migrate.go for the plan-building logic.
func cmdMigrate(args []string, stdout, stderr io.Writer, env Environment) int {
	if len(args) == 0 {
		return argparseRequired(stderr, "migrate", "migrate_target")
	}
	if args[0] != "workspace-resources" {
		return argparseSubError(stderr, "migrate", fmt.Sprintf(
			"argument migrate_target: invalid choice: '%s' (choose from workspace-resources)", args[0]))
	}
	parsed, ok := parseArgparseOpts("migrate workspace-resources", args[1:], []string{"--dry-run"}, nil, nil, 0, stderr)
	if !ok {
		return 2
	}
	dryRun := parsed.flags["--dry-run"]

	aikitoDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	// On a real run, finish or roll back an interrupted migration before
	// planning, under the writer lock. A pending "layout" journal makes
	// every other command refuse to run (see
	// workspace.RequireCurrentLayout), so this is the way out of that state.
	if !dryRun {
		lock, err := writerlock.Acquire(env.Home)
		if err != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", err)
			return 1
		}
		recovered, err := sync.Recover([]string{aikitoDir}, migrationPathPolicy(), nil)
		lock.Release()
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
	for _, c := range plan.Creates {
		fmt.Fprintf(stdout, "[CREATE] %s\n", c.Path)
	}
	for _, u := range plan.Updates {
		fmt.Fprintf(stdout, "[UPDATE] %s\n", u.Path)
	}
	for _, r := range plan.Removes {
		fmt.Fprintf(stdout, "[REMOVE] %s\n", r)
	}
	for _, f := range plan.Findings {
		fmt.Fprintf(stderr, "[BLOCKED] %s\n", f)
	}
	for _, n := range plan.Notes {
		fmt.Fprintf(stdout, "[NOTE] %s\n", n)
	}
	if plan.Blocked() {
		return 1
	}
	if dryRun {
		fmt.Fprintln(stdout, "[DRY RUN] No files changed")
		return 0
	}
	if err := applyMigration(plan, env.Home); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "[SUCCESS] Workspace resource layout migrated")
	return 0
}

// applyMigration mirrors apply_migration: stage every create/update/marker
// write to a temp dir, fingerprint each staged file, build the Change list
// (creates/updates/marker as After-only or Before+After, legacy file
// removals as Before-only), and commit it all through the shared
// transaction engine in one atomic batch.
func applyMigration(plan workspace.MigrationPlan, home string) error {
	policy := migrationPathPolicy()
	lock, err := writerlock.Acquire(home)
	if err != nil {
		return err
	}
	defer lock.Release()
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
		return fmt.Errorf("Workspace changed after migration planning")
	}
	if plan.Blocked() {
		return fmt.Errorf("Migration has blockers; no files changed")
	}
	if len(plan.Removes) == 0 {
		return nil
	}
	// Python creates agents/ itself (default mode), outside the
	// transaction: its migration policy doesn't create parents.
	agentsDir := filepath.Join(plan.Root, "agents")
	if fi, err := os.Lstat(agentsDir); err == nil && (!fi.IsDir() || fi.Mode()&os.ModeSymlink != 0) {
		return fmt.Errorf("Unsafe Agent directory: %s", agentsDir)
	}
	if err := os.Mkdir(agentsDir, 0o777); err != nil && !os.IsExist(err) {
		return err
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
