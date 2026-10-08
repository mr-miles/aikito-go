# Working on aikito-go

Go port of the Python [Aikito](https://github.com/lsaint/aikito) CLI. Read
`UPSTREAM.md` first: it has the upstream baseline commit, the Python → Go
module map, and the backport procedure. `README.md` lists what's implemented
and where behaviour deliberately differs from Python.

## Setup

- The reference Python checkout is expected next to this repo at
  `../aikito` (on the original dev machine: `/home/miles/aikito-rs/aikito`,
  with this repo at `/home/miles/aikito-rs/code`).
- Python is stdlib-only. Call it directly with
  `PYTHONPATH=../aikito/src python3 -m aikito ...`, or import functions with
  `sys.path.insert(0, "../aikito/src")`.
- `scripts/upstream-diff.sh ../aikito` shows upstream changes since the
  baseline and any drift in files copied verbatim from upstream.

## How to verify behaviour

The working rule: **every non-trivial behaviour is checked against live
Python, not against my reading of it.** This caught real bugs nearly every
time it was applied.

- Generate expected values by running the Python function and writing them to
  `testdata/` with a script. Never hand-copy hashes or expected strings: a
  dropped trailing character in hand-typed hashes cost a round of debugging.
- Inspect real bytes (`.encode().hex()`), never `repr()`. Python's `repr()`
  shows ` ` escaped even though `json.dumps(..., ensure_ascii=False)`
  outputs it raw. Go's `%q` has the same problem.
- Negative control (required): every behaviour change needs a test that fails
  without it. Check with `scripts/negative-control.sh <commit> [base]`, which
  runs the commit's tests against the old code. A compile failure only counts
  for new internal APIs; user-visible changes need a black-box test through
  `Run` (or e2e) that compiles against the old code and fails on behaviour
  (see `help_run_test.go`, `syncproject_cli_test.go`).
- Whole-command checks: run both CLIs against identical fresh `$HOME`s and
  `diff -r` the results. The e2e suite (`e2e/`) does this against golden
  fixtures captured from Python once.

## Python → Go traps (each of these caused a real bug)

- **`str.splitlines()` vs `strings.Split(s, "\n")`.** Split leaves a trailing
  empty element after a final newline, keeps `\r` from CRLF, and ignores the
  other line separators Python honours. Use `pythonSplitLines`
  (`internal/workspace/layout.go`) or trim `\r` explicitly. This has bitten
  subagent rendering, project path editing and doctor's conflict-marker scan.
- **`json.dumps` defaults.** Separators are `", "` and `": "`, `sort_keys` is
  explicit, `ensure_ascii=False` emits all non-ASCII raw (including
  U+2028/U+2029), and `<`, `>`, `&` are not escaped. Go's `encoding/json` is
  compact and HTML-escapes. Use `workspace.CanonicalJSON` (sorted, default
  separators) or `mcp.DumpIndented`/`DumpCompactSorted`; never
  `json.Marshal` for anything compared against Python output.
- **JSON numbers.** Python keeps int vs float from the source text; Go decodes
  everything to `float64`. Decode with `UseNumber()`
  (`workspace.DecodeJSONPreservingNumbers`, `DecodeStrictJSON`).
- **Duplicate JSON keys.** Python's `object_pairs_hook` rejects them; Go
  silently keeps the last. Use `workspace.DecodeStrictJSON`.
- **Dict insertion order.** Several MCP adapters re-serialise a whole JSON
  document; Python preserves key order. Use `mcp.OrderedObject`, not
  `map[string]any`.
- **TOML.** go-toml/v2 decodes tables into unordered maps, so the renderers
  in `internal/sync/tomlrender.go` reconstruct top-level key order from the
  original text. Date/time values must fingerprint as Python's
  `{"toml-type": ..., "value": str(x)}`, where `str(datetime)` uses a space,
  not `T`.
- **`str.isprintable()`** equals `unicode.IsPrint` (apart from code points
  added after Go's Unicode tables). A hand-rolled version let U+202E through.
- **RE2 has no lookahead.** Python regexes with `(?=...)` (e.g. wikilink
  matching in `rename memory`) need a hand-written boundary scan.
- **URL parsing.** `urlsplit().hostname` lowercases; Go's `Hostname()` does
  not. `parse_qs` drops blank-valued and bare keys by default; `url.Values`
  keeps them (see `mcp.pythonQueryKeys`). Python's `netloc` includes
  `user@`.
- **`Path.resolve()`** resolves symlinks; the Go equivalent is
  `workspace.ResolvePath`. On macOS temp dirs are under `/var` →
  `/private/var` and `/home` links into `/System/Volumes/Data`, so tests must
  use `resolvedTempDir(t)`, never a raw `t.TempDir()` or a hard-coded
  `/home/...` when comparing paths the code resolves.
- **Subagent instructions** are `body.strip()` in Python. Render from the
  stripped body everywhere (sync and diff both).
- **Quoting in messages.** Python f-strings like `f'chmod 600 "{path}"'`
  don't escape; Go's `%q` does. Match Python literally.
- **Comparing CLI output.** Capture stdout and stderr separately. With both
  piped to one file, Python's buffered stdout lands after its unbuffered
  stderr, which makes the line order look different from what users see.
- **Agent order.** Python iterates agents in sorted `agents/<name>.toml`
  file order; Go's `AgentRegistry` uses policy order (`BuiltinAgents`
  first). Where order is visible (e.g. "Codex/DeepSeek Harness/…" in
  `sync global`), use `reg.InFileOrder()`.
- **Whole-command diffs need a fixed `$HOME` path.** Project skill state
  files are named by a hash of absolute paths, so run both CLIs against the
  same home path (recreated between runs) to compare state files byte for
  byte. Python must also run with `PYTHONUNBUFFERED=1` if stdout and stderr
  share a pipe.
- **JSON error text.** Python prints `json.loads` errors to users
  ("Expecting ',' delimiter: line 3 column 8 (char 19)"). Go's decoder
  wording differs; use `mcp.PythonJSONDecodeError(text)` after a failed
  parse.
- **Generators reading files back.** `Path.read_text()` turns `\r\n` into
  `\n`; open with `newline=""` when snapshotting trees for goldens.
- **Python `str()`/`repr()` in messages.** Values interpolated with `{x!r}`
  or `str(list)` render Python-style (`'a'`, `['a', 'b']`, `True`);
  `cli.pyRepr`/`pyStr` (adopt.go) reproduce that.
- **`Path / raw_link`** keeps `..` components; `filepath.Join` cleans them.
  Messages that show a symlink's destination use `linkplan`'s
  `pathlibJoin`.
- **argparse parent flags.** `aikito sync --dry-run global` is a dry run:
  the parent parser's `--dry-run` stays set on the shared namespace. Flags
  are matched by unique prefix (`--dry`), and an unknown parent flag is a
  root-level "unrecognized arguments" error.
- **Same domain, two entry points.** Python's whole-workspace sync calls the
  same planners as the per-domain commands but with different defaults
  (e.g. `build_global_sync_plan` without `outdated_bundled_skills`, and
  `AIKITO_AGENTS_DIR` for the hub). Port the shared function once with the
  options, not two copies.

## Architecture notes

- **Import direction:** `internal/sync` imports `internal/workspace`, never
  the reverse. That's why the transaction engine and the resource-write
  pipeline live in `sync`, not `workspace` as in Python, and why the read-only
  journal scan is duplicated in `workspace/pending.go`.
- **Plan observation.** Every sync plan has an `Observe()` returning a
  `sync.PlanObservation` (operation views + findings), as Python's
  `observe()`. The bare `aikito sync` summary, "Needs attention" list and
  `--verbose` details are computed only from these, so a new plan kind or
  action needs its effect/finding mapping ported too.
- **Two fingerprint functions on purpose.** `workspace.FingerprintResource`
  hashes raw bytes; `InspectResourceContent` hashes parsed values (for
  agent/mcp/subagent). Both are used in different call paths, as in Python.
  Don't unify them.
- **All workspace writes go through `sync.Apply`** (journal + staging + TOCTOU
  re-checks + `Recover`). Symlinks inside managed paths are always unsafe.
  The journal records its `PathPolicy`, and `Recover` requires the caller's
  policy to match, so share one policy function per operation (e.g.
  `migrationPathPolicy`).
- **Agent detection:** if an agent definition has a `detect` table, it's
  installed only if a listed command is on `PATH` or a listed marker dir exists
  under home (e.g. `.claude` for Claude Code), and never "unknown". Tests must
  create the marker dir. The original dev sandbox has a real `claude` binary
  on `PATH`, which hid a broken test until CI.
- `config:inbox.path` is host-local (`LocalConfig`); it is scanned but never
  synced or imported. Bundled skill names (`aikito`, `durable-memory`) are
  skipped as resources and excluded from reference checks.
- The Python package exposes a public library API (`Project`, `Workspace`,
  … in `__init__.py`); the port does not. Everything is under `internal/` and
  the CLI calls the building blocks directly.

## Conventions

- Commands are `func cmdX(args, stdout, stderr, env Environment) int`,
  dispatched from `internal/cli/run.go`. Command logic must read environment
  variables through `env.Env.Getenv`, not `os.Getenv`. Known exception:
  doctor's `checkEnvironment`.
- When adding a command, add it to `cliSchema` in `completion.go`;
  `completion_test.go` fails otherwise.
- Unfinished options print a message saying they're not implemented. Never
  silently ignore a flag. Keep README's Status section in sync, and the
  `helpAnnotations` in `help.go` (they mark those options in `--help`; remove
  the marker when an option is implemented).
- `--help` text is Python's, captured verbatim into `helptext/help.json`.
  Don't hand-edit it; regenerate (see UPSTREAM.md).
- In-process CLI tests use `testEnv(t)` (restricted `PATH`, resolved temp
  home). e2e tests run the real binary against `e2e/testdata/` goldens.
- Version: `cli.Version` is stamped by GoReleaser via `-ldflags -X`;
  `go install @vX` uses the module version; plain builds report `0.1.0-dev`.

## Reproducing CI locally

Go's test cache and this machine's environment can hide failures. Before
saying tests pass:

```bash
rm -rf /tmp/ciclone && git clone -q . /tmp/ciclone/code && cd /tmp/ciclone/code
env -i HOME=$(mktemp -d) PATH="$(dirname $(which go)):/usr/bin:/bin" \
  GOCACHE=/tmp/ci-gocache GOPATH=/tmp/ci-gopath go test -count=1 ./...
# macOS-style symlinked temp dirs:
mkdir -p /tmp/realtmp && ln -sfn /tmp/realtmp /tmp/symtmp   # then add TMPDIR=/tmp/symtmp
```

There's no C compiler in the original sandbox, so `-race` only runs in CI
(started by hand, along with macOS and Windows). Windows has never been
debugged locally; expect path-separator and `PATH` issues there first.

## Open gaps worth knowing before changing nearby code

- `project.DetectCurrentProject` exists now, but `maintain memory .` and
  `edit instructions` (no target) still don't use it.
- Python's `execute_selection_transaction` (used by `rm skill` and
  `add skill --sync` to deactivate copied-skill state) is not ported, so
  removing a skill in Go leaves its project state record active until the
  next `sync project`.
- `import workspace` compares against current templates only, with no
  `TEMPLATE_HISTORY` (Python's `adopt` doesn't use it either).
- `init workspace` initialises over a non-empty directory that isn't a
  workspace; Python refuses ("Target directory is not empty and is not a
  recognized Aikito workspace").
- `add.go` still has its own simplified frontmatter parser;
  `workspace.ParseMarkdownFrontmatter` is the faithful port.
- `doctor --fix` cleans stale local state only; the registry backfill
  (`registry.py` add_missing_agent_fields) is not ported.
- `atomicUnlink` and `PendingKinds` have no callers in the write path yet. A
  few `compat` helpers are unused.
- status/doctor/show read plans through `internal/cli/inspection.go`
  (WorkspaceInspectionContext). Their output is checked byte-for-byte
  against Python by `report_vectors.json`; regenerate after changing them.
- Not ported: `aikito web`, multi-machine sync (`workspace/remote*`), and the
  PyPI update check.
