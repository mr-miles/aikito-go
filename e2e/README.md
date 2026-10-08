# e2e: golden-fixture tests against the reference Python implementation

This suite confirms the Go `aikito` binary produces the same resulting
file trees (and, in a few cases, functionally-equivalent output) as the
reference Python implementation ([lsaint/aikito](https://github.com/lsaint/aikito))
for a set of known scenarios: `init workspace`/`init project`, `add
skill|subagent|mcp`, `adopt`, `rm skill|mcp`, `show skills|mcps`, `sync
global|mcp|subagents` (including create/re-run-noop/conflict/--force
lifecycles).

## Design: capture once, compare forever

Earlier versions of this suite ran Python live, side-by-side with the Go
binary, on every test invocation. That's unnecessary: Python's behavior for
a given scenario doesn't change test-to-test, so there's no need to pay for
a live interpreter + reference checkout on every `go test` run (and every
CI run, on every OS, in every PR). Instead:

1. **Golden fixtures are captured once** by actually running the reference
   Python CLI against each scenario and saving its resulting file tree
   under `e2e/testdata/<scenario>/` (a `manifest.json` describing every
   path's kind — file/dir/symlink — plus a `files/` subtree mirroring real
   file content, directly reviewable and diffable in git).
2. **The normal test suite** (`go test -tags e2e ./e2e/...`) builds and
   runs *only* the Go binary, then compares its output against those
   committed goldens. It needs no Python interpreter and no reference
   checkout at all — confirmed by running the suite with `python3` removed
   from `PATH` entirely.
3. **CI** just runs `go test -tags e2e ./e2e/...` as an ordinary step in
   the main build/test job, on every OS in the matrix.

## Regenerating golden fixtures

Only needed when adding a new scenario, or after confirming an
*intentional* Python behavior change should be reflected in the goldens.
Requires `python3` on `PATH` and a reference checkout of
[lsaint/aikito](https://github.com/lsaint/aikito) (either a sibling
directory `../aikito` next to this repo, or point `AIKITO_PYTHON_SRC` at
its `src/` directory):

```bash
# from the module root
AIKITO_PYTHON_SRC=/path/to/aikito/src go test -tags e2e_generate -run TestGenerateGoldens ./e2e/... -v
```

This overwrites `e2e/testdata/<scenario>/` for every scenario defined in
`generate_test.go`. Review the resulting diff like any other change before
committing — a golden diff you didn't expect usually means either a real
regression in the Go port or a genuine Python behavior change worth calling
out in the commit message, not something to accept blindly.

## Files

- `common_test.go` (tag `e2e || e2e_generate`): shared helpers used by both
  the test suite and the generator — subprocess running, directory-tree
  snapshotting (`treeManifest`), golden fixture save/load, and comparison
  (`compareManifests`/`compareAgainstGolden`).
- `harness_test.go` (tag `e2e`): builds the real Go binary once
  (`TestMain`) and provides `runGo`.
- `generate_test.go` (tag `e2e_generate`): the generator. Never run by CI
  or by the normal `-tags e2e` suite.
- `{init,add,adopt,rm,show,sync}_test.go` (tag `e2e`): the actual scenario
  tests.
- `testdata/`: committed golden fixtures.

## Command output goldens

Some scenarios (`sync_global_output`, `sync_global_prepopulated_output`)
also capture the command's exit code, stdout and stderr as `output.txt`,
with the temp home replaced by `H` (see `outputGolden`). A symlink compared
as a tree root (e.g. `~/.claude/skills`) is recorded as a single symlink
entry, so a real directory in its place fails the comparison.

The full `sync global` behaviour matrix (conflicts, stale entries, legacy
layouts, dry runs, bundled-skill refresh) is covered in-process by
`internal/cli/syncglobal_test.go` against vectors generated from Python.

Whole-workspace `aikito sync` scenarios (`workspacesync_common_test.go`)
reuse the project-sync transcript harness: `generate_workspacesync_test.go`
captures them from Python into `testdata/workspace_sync_*`, and
`workspacesync_test.go` replays them. Regenerate with
`go test -tags e2e_generate -run TestGenerateWorkspaceSyncGoldens ./e2e/... -v`.
The full matrix (conflicts in every domain, broken configuration, bundled
refresh, parent flags) is in `internal/cli/syncall_test.go`.

## Interoperability (`interop_*_test.go`)

These check that Python and Go can take over each other's workspace and
runtime state. `generate_interop_test.go` builds each scenario in
`ioScenarios` twice, with Python and with the Go binary, and snapshots the
home into `testdata/interop/<scenario>/{python_state,go_state}/`. A
snapshot is relocatable: the home directory becomes `<HOME>`, project-skill
state file names and binding hashes (hashes of absolute paths) become
`<BINDING:project:checkout>`, transaction ids and backup timestamps are
fixed, and the workspace's `.git` is replaced by a fresh `git init` on
restore. Python's journal `affected_skills` (built from a set) is sorted,
and runs of migration "Target already exists" lines are sorted (Python
lists `agents/` in filesystem order). Restored files get a fixed mtime and
commands run with `TZ=UTC`.

The generator then restores each snapshot into a fresh home and runs the
scenario's commands with Python, recording `python_on_python.txt` and
`python_on_go.txt` (transcripts) and the resulting homes. The tests, which
need no Python:

- `TestInteropGoOnPythonState` restores `python_state`, runs the commands
  with Go, and requires Python's transcript and resulting home; scenarios
  marked `clean` must also be left unchanged.
- `TestInteropGoWritesPythonState` builds each scenario with Go and requires
  Python's build transcript and an identical home, checks the recorded
  `go_state` is still what Go builds (otherwise regenerate), and checks
  Python's recorded view of the Go-built home equals its view of its own.

Interrupted scenarios stop the last build step part-way: Python through
`testdata/interop/python_fault.py`, Go through a binary built with
`-tags aikito_faultinject` (see `internal/faultinject`), both driven by
`AIKITO_FAULT=<point>:<n>`.

```bash
AIKITO_PYTHON_SRC=../aikito/src go test -tags e2e_generate -run TestGenerateInteropGoldens ./e2e/... -v
```
