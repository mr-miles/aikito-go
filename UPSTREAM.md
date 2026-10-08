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
| `workspace/templates.py` | — | not ported (`TEMPLATE_HISTORY` is used only by `import workspace`/reconcile, not adopt) |
| `workspace/sync.py` (`build_workspace_sync_plan`, `execute_workspace_sync_plan`), `render.py` `render_workspace_sync_plan`, `cli.py` `cmd_sync_all` | `internal/cli/syncworkspace.go`, `sync.go` (parent-flag dispatch) | ported |
| `observe()` of each plan (`link.py`, `global_skills.py`, `instructions.py`, `memory_runtime.py`, `skill_plan.py`, `project_sync.py`, `subagent.py`, `mcp/model.py`) | `Observe` methods in `internal/linkplan/observe.go`, `internal/projectsync/observe.go`, `internal/sync/subagents_observe.go`, `internal/mcp/observe.go` | ported |
| `subagent.py` `execute_subagent_plan` (per-file writes, backups) | `internal/sync/subagents_apply.go`, `filesnapshot.go` (`FileSnapshot`, `capture_file_snapshot`) | ported |
| `workspace/inspection.py`, `inspection.py` (and each plan's `inspect()`) | `internal/cli/inspection.go`, `internal/linkplan/inspect.go`, `internal/cli/projectmemoryviews.go` | ported |
| `agents.py` | `internal/registry/` | ported (`resolve_targets` in `targets.go`) |
| `registry.py` (agent schema migration), `doctor.py` `run_doctor_fixes` | `internal/cli/agentfix.go`, `agentfields.go`, `doctor.go` | ported |
| `config.py`, `config_runtime.py` | `internal/workspace/resources.go`, `internal/mcp/configtarget.go` | partial: physical-identity resolution simplified |
| `project_config.py`, `project_runtime.py`, `project.py` | `internal/project/` | ported (config layer); `project.py`'s status layer is in `internal/cli/status.go` |
| `project_sync.py`, `skill_plan.py`, `skill_runtime.py` (incl. `execute_selection_transaction` in `selection.go`), `skill_state.py` (state, journal, recovery) | `internal/projectsync/` | ported; state documents and journals are interchangeable with Python's |
| `memory_runtime.py` (project runtime), `conflict.py` (`collect_resource_conflicts`) | `internal/projectsync/memory.go`, `conflict.go` | ported |
| `init.py` `project_validation_error`/`project_sync_validation_error`, `resolve.py` `detect_current_project` | `internal/projectsync/sync.go` (`ValidationError`), `internal/project/detect.go` | ported |
| `global_skills.py`, `instructions.py`, `link.py` | `internal/linkplan/` | ported; `build_project_instruction_batch` is in `projectinstructions.go` and drives `sync project` |
| `workspace/sync.py` (`build_global_sync_plan`, `execute_global_sync_plan`, bundled refresh), `cli.py` `sync_global_resources` | `internal/cli/syncglobalplan.go`, `syncglobal.go`, `bundledrefresh.go` | ported |
| `skill_state.py` `WorkspaceWriterLock` | `internal/writerlock/` | ported |
| `subagent.py`, `subagent_adapters.py`, `subagent_validation.py` | `internal/subagent/`, `internal/sync/subagents.go` | ported |
| `mcp/model.py`, `mcp/adapters/*` | `internal/mcp/model.go`, `adapters.go`, `toml.go`, `jsonc.go`, `cordis.go`, `orderedjson.go` | ported |
| `mcp/loader.py`, `planner.py`, `executor.py` | `internal/mcp/loader.go`, `planner.go`, `executor.go`, `state.go` | ported |
| `mcp/auth.py`, `probe.py`, `redact.py` | `internal/mcp/auth.go`, `probe.go`, `redact.go` | ported |
| `mcp/__init__.py` (`sync_mcp_configs`) | `internal/cli/sync.go` | ported |
| `cli.py`, `cli_parser.py` | `internal/cli/run.go` and one file per command | ported, option gaps listed in README |
| `cli_show.py`, `resolve.py`, `memory.py`, `inbox.py` | `internal/cli/show.go`, `edit.go`, `rm.go`, `rename.go` | ported |
| `adopt.py`, `cli.py` `cmd_adopt`, `doctor.py` `check_adoption` | `internal/cli/adopt.go`, `doctor.go` (`checkAdoption`) | ported |
| `add.py`, `remove.py`, `init.py`, `templating.py`, `bundled_skills.py` | `internal/cli/add.go`, `addskill.go`, `skillimport.go`, `addmcp.go`, `addsubagent.go`, `sanitize.go`, `rm.go`, `rmskill.go`, `init.go` | ported |
| `status.py`, `render.py`, `context_footprint.py`, `project.py` (summaries, health) | `internal/cli/status.go`, `statusagents.go`, `statusmemory.go`, `projectsummary.go`, `subagentmatrix.go`, `render.go`, `table.go`, `showdetails.go` (`--agent` detail views, `--live`) | ported (no animated loading line for `--live`) |
| `diff.py`, `diff_model.py`, `project.py` `collect_project_skill_diffs` | `internal/cli/diff.go`, `unifieddiff.go` | ported |
| `doctor.py`, `conflict.py`, `local_state.py` | `internal/cli/doctor.go`, `localstate.go`, `agentfields.go` | ported (`--fix` cleans stale local state; the agent-registry backfill is not ported; the interpreter-consistency check is Python-only) |
| `maintain.py`, `memory_runtime.py` | `internal/cli/maintain.go`, `rm.go` | ported |
| `completion.py`, `completion_powershell.py` | `internal/cli/completion.go` | ported (hand-maintained schema) |
| `update_notifier.py` | `internal/cli/version.go` | partial: no PyPI update check |
| `compat.py` | `internal/compat/`, plus helpers spread across packages | partial |
| `frontmatter.py` | `internal/workspace/frontmatter.go` (`_parse_markdown_frontmatter`), `frontmatter_update.go` (`_update_markdown_frontmatter`, `_format_yaml_scalar`) | ported |
| CPython `json` error messages (`_json` scanner) | `internal/mcp/pyjsonerror.go` | ported for MCP JSON config errors |
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
| `internal/cli/testdata/syncglobal_vectors.json` | `python3 internal/cli/testdata/gen_syncglobal_vectors.py` |
| `internal/cli/testdata/initworkspace_vectors.json` (`init workspace` refusals, hints, refresh) | `python3 internal/cli/testdata/gen_initworkspace_vectors.py` |
| `internal/cli/testdata/subagent_cli_vectors.json` (symlinked subagent files, `add`/`rm subagent --sync`) | `python3 internal/cli/testdata/gen_subagent_cli_vectors.py` |
| `internal/sync/testdata/subagent_stale_vectors.json` (stale subagent plans) | `python3 internal/sync/testdata/gen_subagent_stale_vectors.py` |
| `internal/cli/testdata/syncall_vectors.json` (bare `aikito sync`, parent flags, `sync subagents` backups) | `python3 internal/cli/testdata/gen_syncall_vectors.py` |
| `internal/registry/testdata/targets_vectors.json` | `python3 internal/registry/testdata/gen_targets_vectors.py` |
| `internal/cli/testdata/report_vectors.json` (status, doctor, show) | `python3 internal/cli/testdata/gen_report_vectors.py`; compare a binary without writing: `... --compare ./aikito [scenario...]` |
| `internal/cli/testdata/doctorfix_vectors.json` (`doctor --fix` output and resulting agent files) | `python3 internal/cli/testdata/gen_doctorfix_vectors.py` |
| `internal/cli/testdata/mcplive_vectors.json` (`show mcp --live` against a local fixture server; `mcplive_test.go` serves the same responses) | `python3 internal/cli/testdata/gen_mcplive_vectors.py` |
| `internal/cli/testdata/adopt/adopt_vectors.json` (33 whole-command `adopt` scenarios) | `AIKITO_PYTHON_SRC=../aikito/src python3 internal/cli/testdata/adopt/gen_adopt_vectors.py`; `--compare ./aikito` diffs a built Go binary against them |
| `internal/cli/testdata/adopt_mcp_secrets/want/` | `internal/cli/testdata/adopt_mcp_secrets/gen.sh` |
| `internal/mcp/testdata/pyjson_errors.json` (CPython `json.loads` messages) | `python3 internal/mcp/testdata/gen_pyjson_errors.py` (captured with CPython 3.14; wording can differ between Python versions) |
| `internal/projectsync/testdata/vectors.json` (`plan_single_skill`, `plan_link_target`, fingerprints, binding hash, state JSON) | `python3 internal/projectsync/testdata/gen_vectors.py` |
| `e2e/testdata/project_sync_*` (whole-command transcripts and trees) | `AIKITO_PYTHON_SRC=../aikito/src go test -tags e2e_generate -run TestGenerateProjectSyncGoldens ./e2e/... -v` |
| `e2e/testdata/workspace_sync_*` (bare `aikito sync` transcripts and trees) | `AIKITO_PYTHON_SRC=../aikito/src go test -tags e2e_generate -run TestGenerateWorkspaceSyncGoldens ./e2e/... -v` |
| `e2e/testdata/interop/*` (Python- and Go-built homes, each implementation's commands on the other's, interrupted transactions; see e2e/README.md) | `AIKITO_PYTHON_SRC=../aikito/src go test -tags e2e_generate -run TestGenerateInteropGoldens ./e2e/... -v` |
| `internal/cli/testdata/options_vectors.json` (add/rm/maintain/edit/diff options, run from inside projects) | `AIKITO_PYTHON_SRC=../aikito/src python3 internal/cli/testdata/gen_options_vectors.py` |
| `internal/workspace/testdata/frontmatter_update_vectors.json` | `AIKITO_PYTHON_SRC=../aikito/src python3 internal/workspace/testdata/gen_frontmatter_update_vectors.py` |
| `internal/mcp/testdata/toml_order_vectors.json` (tomllib key order) | `python3 internal/mcp/testdata/gen_toml_order_vectors.py` |
| `internal/cli/testdata/skill_description_vectors.json` | `python3 internal/cli/testdata/gen_skill_description_vectors.py` |
| `internal/cli/testdata/rm_usage_vectors.json` | `python3 internal/cli/testdata/gen_rm_usage_vectors.py` |
| `internal/compat/testdata/utf8_replace_vectors.json` (CPython `decode("utf-8", "replace")`) | `python3 internal/compat/testdata/gen_utf8_replace_vectors.py` |
| `internal/cli/testdata/diff_invalid_utf8/` | `python3 internal/cli/testdata/diff_invalid_utf8/gen.py` |
| `internal/cli/testdata/subagent_load_error_vectors.json` | `python3 internal/cli/testdata/gen_subagent_load_errors.py` |
| `internal/workspace/testdata/strictjson_vectors.json` (`json.loads` with strict hooks) | `python3 internal/workspace/testdata/gen_strictjson_vectors.py` |
| `internal/compat/testdata/float_repr_vectors.json` (CPython `repr(float)`) | `python3 internal/compat/testdata/gen_float_repr_vectors.py` |
| `internal/cli/testdata/mcp_float_vectors.json` | `python3 internal/cli/testdata/gen_mcp_float_vectors.py` |
| `internal/cli/helptext/help.json` (embedded `--help` text) | `python3 internal/cli/helptext/gen_help.py`, then fix any marker `help_test.go` reports in `helpAnnotations` (`help.go`) |

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
