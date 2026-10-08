# Upstream tracking

This repo is a Go port of the Python [Aikito](https://github.com/lsaint/aikito).
This file records exactly which upstream version it matches, what was copied
verbatim, which Python module each Go file ports, and how to backport later
upstream changes. Update the baseline below after every backport.

## Baseline

- **Repository:** https://github.com/lsaint/aikito
- **Release:** `v1.57.7` (Python `aikito.__version__ == "1.57.7"`)
- **Commit:** `a24df1c12f5557918a76c4a5dc68a6d7c293bc1b`
- **Commit date:** 2026-10-08 10:00:55 +0800 ("release: v1.57.7"), branch `main`
- **Ported:** 2026-10-08, from a clean checkout of that commit

Behaviour, golden test data and copied files all correspond to this commit.

## Copied verbatim from upstream

These must stay byte-identical to upstream. `scripts/upstream-diff.sh` reports
any that drift.

| Upstream path | Go repo path | Notes |
|---|---|---|
| `src/aikito/templates/agents/*.toml` | `internal/registry/templates/agents/` | The 8 bundled agent definitions plus `_header.toml` |
| `src/aikito/templates/{config.toml,gitignore,skills.toml,global/,project/}` | `internal/cli/templates/` | Written by `init workspace` / `init project` |
| `src/aikito/templates/skills/{aikito,durable-memory}/` | `internal/cli/templates/skills/` | Bundled skills |
| `src/aikito/templates/layout.toml` | not copied | Hard-coded as `LayoutContent` in `internal/workspace/layout.go` |
| `src/aikito/templates/subagents.toml` | not copied | Legacy-layout file; only read by `migrate` |
| `docs/` | `docs/` | Except `docs/README.md`, which is ours |
| `LICENSE` | `LICENSE` | |

## Module map

Status: **ported** (behaviour matches), **partial** (ported with documented
gaps; see the Go file's doc comment), **not ported**.

| Python (`src/aikito/`) | Go | Status |
|---|---|---|
| `workspace/resources.py` | `internal/workspace/resources.go`, `fingerprint.go`, `names.go` | ported |
| `workspace/resource_state.py` | `internal/workspace/resourcestate.go` | ported |
| `workspace/layout.py` | `internal/workspace/layout.go`, `migrate.go`, `internal/cli/migrate.go` | ported |
| `workspace/paths.py` | `internal/workspace/paths.go` | ported |
| `workspace/skill_metadata.py`, `skill_artifacts.py` | `internal/workspace/fingerprint.go`, `resources.go` | ported |
| `workspace/transactions.py` | `internal/sync/transactions.go`, `internal/workspace/pending.go` | ported (no v1-journal compat; Windows reparse points stubbed) |
| `workspace/merge.py` | `internal/sync/merge.go` | ported |
| `plan_observation.py`, `diagnostics.py` | `internal/sync/plan_observation.go` | ported |
| `workspace/resource_write.py` | `internal/sync/resourcewrite.go` | ported |
| `workspace/toml_render.py`, TOML helpers in `add.py` | `internal/sync/tomlrender.go` | ported (top-level key order reconstructed, not preserved) |
| `workspace/importing.py`, `import_decisions.py` | `internal/cli/importworkspace.go` | partial |
| `workspace/templates.py` | `internal/cli/adopt.go` (`templateFingerprints`) | partial: current templates only, no `TEMPLATE_HISTORY` |
| `workspace/sync.py` | `internal/cli/sync*.go` | partial: per-domain commands, no whole-workspace plan |
| `workspace/inspection.py` | `internal/cli/status.go`, `doctor.go` | partial |
| `agents.py` | `internal/registry/` | partial: `resolve_targets` not ported (`targets_todo.go`) |
| `registry.py` (agent schema migration) | — | not ported (`doctor --fix` backfill) |
| `config.py`, `config_runtime.py` | `internal/workspace/resources.go`, `internal/mcp/configtarget.go` | partial: physical-identity resolution simplified |
| `project_config.py`, `project_runtime.py`, `project.py` | `internal/project/` | ported (config layer); `project.py`'s status layer is in `internal/cli/status.go` |
| `project_sync.py`, `skill_plan.py`, `skill_runtime.py`, `skill_state.py` | `internal/sync/projectskills.go` | partial: simplified state file, no CAS/revision engine |
| `global_skills.py`, `instructions.py`, `link.py` | `internal/sync/globalskills.go`, `globalinstructions.go`, `link.go` | partial: per-agent links, no shared `~/.agents/skills` hub |
| `subagent.py`, `subagent_adapters.py`, `subagent_validation.py` | `internal/subagent/`, `internal/sync/subagents.go` | ported |
| `mcp/model.py`, `mcp/adapters/*` | `internal/mcp/model.go`, `adapters.go`, `toml.go`, `jsonc.go`, `cordis.go`, `orderedjson.go` | ported |
| `mcp/loader.py`, `planner.py`, `executor.py` | `internal/mcp/loader.go`, `planner.go`, `executor.go`, `state.go` | ported |
| `mcp/auth.py`, `probe.py`, `redact.py` | `internal/mcp/auth.go`, `probe.go`, `redact.go` | ported |
| `mcp/__init__.py` (`sync_mcp_configs`) | `internal/cli/sync.go` | ported |
| `cli.py`, `cli_parser.py` | `internal/cli/run.go` and one file per command | ported, option gaps listed in README |
| `cli_show.py`, `resolve.py`, `memory.py`, `inbox.py` | `internal/cli/show.go`, `edit.go`, `rm.go`, `rename.go` | ported |
| `add.py`, `adopt.py`, `remove.py`, `init.py`, `templating.py`, `bundled_skills.py` | `internal/cli/add.go`, `sanitize.go`, `adopt.go`, `rm.go`, `init.go` | partial (see README Status) |
| `status.py`, `render.py`, `context_footprint.py` | `internal/cli/status.go`, `table.go` | partial |
| `diff.py`, `diff_model.py` | `internal/cli/diff.go`, `unifieddiff.go` | partial (`diff project` missing) |
| `doctor.py`, `conflict.py`, `local_state.py` | `internal/cli/doctor.go` | partial (LocalState stub, no `--fix` fixes) |
| `maintain.py`, `memory_runtime.py` | `internal/cli/maintain.go`, `rm.go` | partial |
| `completion.py`, `completion_powershell.py` | `internal/cli/completion.go` | ported (hand-maintained schema) |
| `update_notifier.py` | `internal/cli/version.go` | partial: no PyPI update check |
| `compat.py` | `internal/compat/`, plus helpers spread across packages | partial |
| `frontmatter.py` | `internal/cli/add.go` | partial |
| `__init__.py`, `workspace/api.py` (public Python API) | — | not ported; the port is CLI-only |
| `web_console.py` (`aikito web`) | — | not ported |
| `workspace/remote*.py`, `reconcile*.py`, `serialized_remote.py`, `http_transport.py`, `payload*.py`, `pending_commit.py`, `commit_recovery.py`, `replica_state.py` | — | not ported (multi-machine sync, deferred) |

## Test data generated from upstream

Expected values in these tests were produced by running the baseline Python
code. If the corresponding upstream module changes, regenerate and review the
diff.

| Test data | Regenerate with |
|---|---|
| `e2e/testdata/` | `AIKITO_PYTHON_SRC=../aikito/src go test -tags e2e_generate -run TestGenerateGoldens ./e2e/... -v` |
| `internal/mcp/testdata/redact_vectors.json` | `python3 internal/mcp/testdata/gen_redact_vectors.py` |
| `internal/cli/testdata/unified_diff_vectors.json` | `python3 internal/cli/testdata/gen_unified_diff_vectors.py` |
| `internal/cli/testdata/completion_vectors.json` | `python3 internal/cli/testdata/gen_completion_vectors.py` |

These were generated with one-off scripts that weren't kept. Regenerate by
calling the named Python function directly (`sys.path.insert(0, "src")`):

| Test data | Python source |
|---|---|
| `internal/workspace/fingerprint_test.go` hashes | `workspace/resources.py` `value_fingerprint` |
| `internal/workspace/testdata/snapshot_fixture_expected.json` | `workspace/resources.py` `snapshot_workspace` |
| `internal/workspace/testdata/local_resource_for_id_expected.json` | `workspace/resource_state.py` `local_resource_for_id` |
| `internal/registry/bundled_test.go` field values | `agents.py` `bundled_agent_definition` |
| `internal/subagent/testdata/`, `internal/mcp/testdata/` (other files) | `subagent_adapters.py`, `mcp/adapters/*.py` |
| `internal/workspace/migrate_test.go` edge cases | `workspace/layout.py` `build_migration_plan` |

## Backport procedure

1. Update the upstream checkout (`../aikito`) and see what changed:

   ```bash
   scripts/upstream-diff.sh ../aikito            # baseline .. origin/main
   scripts/upstream-diff.sh ../aikito v1.58.0    # or a specific release
   ```

   It lists upstream commits and changed files since the baseline, and any
   verbatim copies (templates, bundled skills, agent definitions, docs,
   LICENSE) that now differ.
2. Re-copy any drifted verbatim files.
3. For each changed Python module, find its Go counterpart in the module map
   above and port the change. Upstream tests that changed (`tests/`) show the
   intended behaviour. Most Go files cite the Python function they port in a
   comment, so `grep -rn "<python_function_name>" internal/` finds the spot.
4. Regenerate any affected test data (table above), review the diff, and add
   Go tests for new behaviour, with expected values taken from the Python code.
5. Run `go test ./...` and `go test -tags e2e ./e2e/...`.
6. Update the Baseline section above to the new upstream commit, and note the
   upstream version in the commit message, e.g. `Backport upstream v1.58.0`.
