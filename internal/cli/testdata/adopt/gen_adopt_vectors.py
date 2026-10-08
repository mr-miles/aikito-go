"""Generate adopt_vectors.json by running the reference Python CLI.

Each scenario builds a fresh HOME from `files`, runs `steps` (CLI argument
lists) in order, and records each step's stdout, stderr and exit code, plus
the final tree of HOME (workspace .git and bundled skills excluded).
`init workspace` steps record only the exit code; "__write__"/"__append__"
steps ([op, path, text]) are setup that edits a file under HOME.
Paths under HOME are written as "H", and adopt backup timestamps as "TS".

Run from the repo root:
    AIKITO_PYTHON_SRC=../aikito/src python3 internal/cli/testdata/adopt/gen_adopt_vectors.py
Compare a Go binary against the same scenarios (no file written):
    python3 internal/cli/testdata/adopt/gen_adopt_vectors.py --compare ./aikito
"""
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
FAKE = "fake-value-for-tests-0000000000"

CLAUDE_MD = "# My rules\n\nBe nice.\n"
MEMORY_TAIL = (
    "## Persistent Memory\n\n- All tasks must follow the `durable-memory` skill"
)

STDIO_SECRET = {
    "mcpServers": {
        "stdio-s": {
            "command": "npx",
            "args": ["-y", "srv"],
            "env": {"API_KEY": FAKE, "PLAIN": "x", "REF": "${ALREADY}", "N": 3},
        },
        "http-s": {
            "type": "http",
            "url": "https://example.com/mcp",
            "headers": {
                "Authorization": "Bearer " + FAKE,
                "X-Plain": "y",
                "X-Api-Key": "${MY_KEY}",
            },
        },
    }
}

SUBAGENT_CLAUDE = (
    "---\nname: reviewer\ndescription: Reviews code carefully\nmodel: opus\n---\n\n"
    "You review code.\n\nBe thorough.\n"
)
SUBAGENT_COPILOT = (
    "---\nname: reviewer\ndescription: Copilot reviewer\nmodel: gpt-5\n"
    "tools: ['read', 'search']\ndisable-model-invocation: true\ntarget: vscode\n---\n"
    "Copilot review body.\n"
)

# (name, files relative to HOME, steps). A file value of None makes a dir.
SCENARIOS = [
    ("plain_claude_md", {".claude/CLAUDE.md": CLAUDE_MD},
     [["init", "workspace"], ["adopt"], ["adopt"]]),
    ("plain_verbose", {".claude/CLAUDE.md": CLAUDE_MD},
     [["init", "workspace"], ["adopt", "--verbose"], ["adopt", "--verbose"]]),
    ("dry_run", {".claude/CLAUDE.md": CLAUDE_MD, ".claude.json": json.dumps(STDIO_SECRET)},
     [["init", "workspace"], ["adopt", "--dry-run"], ["adopt", "--dry-run", "--verbose"]]),
    ("nothing_to_adopt", {".claude": None},
     [["init", "workspace"], ["adopt"], ["adopt", "--verbose"], ["adopt", "--dry-run"]]),
    ("claude_codex_agree",
     {".claude/CLAUDE.md": CLAUDE_MD, ".codex/AGENTS.md": CLAUDE_MD + "\n\n"},
     [["init", "workspace"], ["adopt", "--verbose"]]),
    ("claude_codex_disagree",
     {".claude/CLAUDE.md": CLAUDE_MD, ".codex/AGENTS.md": "# Other\n"},
     [["init", "workspace"], ["adopt"], ["adopt", "--dry-run", "--verbose"],
      ["adopt", "--skip", "instructions", "--verbose"]]),
    ("instructions_already_have_memory",
     {".claude/CLAUDE.md": CLAUDE_MD + "\n" + MEMORY_TAIL
      + " as the single source of truth for durable memory boundaries, retrieval,"
      " evaluation, and persistence.\n"},
     [["init", "workspace"], ["adopt", "--verbose"]]),
    ("canonical_customized", {".claude/CLAUDE.md": CLAUDE_MD},
     [["init", "workspace"], ["__write__", "aikito/global/AGENTS.md", "# Custom canonical\n"],
      ["adopt"], ["adopt", "--skip", "instructions"]]),
    ("canonical_matches_import", {".claude/CLAUDE.md": CLAUDE_MD},
     [["init", "workspace"], ["__write__", "aikito/global/AGENTS.md", "# My rules\n\nBe nice."],
      ["adopt", "--verbose"]]),
    ("blank_instructions", {".claude/CLAUDE.md": "  \n\n"},
     [["init", "workspace"], ["adopt"]]),
    ("legacy_agy_instructions", {".gemini/config/AGENTS.md": CLAUDE_MD, ".claude": None},
     [["init", "workspace"], ["adopt", "--verbose"]]),
    ("mcp_secrets", {".claude": None, ".claude.json": json.dumps(STDIO_SECRET)},
     [["init", "workspace"], ["adopt", "--verbose"], ["adopt", "--verbose"]]),
    ("mcp_multi_agent",
     {".claude": None, ".codex": None,
      ".claude.json": json.dumps({"mcpServers": {"my-srv": {"command": "srv", "args": ["--x"]},
                                                "web": {"type": "http", "url": "https://a.example/mcp"}}}),
      ".codex/config.toml": '[mcp_servers.my_srv]\ncommand = "srv"\nargs = ["--x"]\n\n'
                            '[mcp_servers.only_codex]\ncommand = "oc"\n',
      ".copilot/mcp-config.json": json.dumps({"mcpServers": {"web": {"type": "http", "url": "https://b.example/mcp"}}})},
     [["init", "workspace"], ["adopt", "--verbose"], ["adopt", "--skip", "mcp/web", "--verbose"]]),
    # codex's config appears after init workspace, so codex is unregistered.
    ("mcp_registration", {".claude": None},
     [["init", "workspace"],
      ["__write__", ".codex/config.toml", '[mcp_servers.codex-only]\ncommand = "oc"\n'],
      ["__write__", ".copilot/agents/helper.agent.md", "---\ndescription: helps\n---\nHelp body\n"],
      ["adopt", "--skip", "agent/codex"], ["adopt", "--dry-run", "--verbose"], ["adopt", "--verbose"]]),
    ("mcp_existing_canonical",
     {".claude": None,
      ".claude.json": json.dumps({"mcpServers": {"have-it": {"command": "a"}, "new-one": {"command": "b"}}})},
     [["init", "workspace"], ["__write__", "aikito/mcps/have-it.toml", 'agents = ["claude-code"]\ncommand = "old"\n'],
      ["adopt", "--verbose"]]),
    ("mcp_invalid_name",
     {".claude": None, ".claude.json": json.dumps({"mcpServers": {"bad/name": {"command": "a"}}})},
     [["init", "workspace"], ["adopt"], ["adopt", "--skip", "mcp/bad/name"]]),
    ("mcp_malformed_source", {".claude": None, ".claude.json": "{not json"},
     [["init", "workspace"], ["adopt"]]),
    ("claude_desktop",
     {".claude": None,
      ".claude/claude_desktop_config.json": json.dumps({"mcpServers": {"desk": {"command": "d", "env": {"TOKEN": FAKE}}}})},
     [["init", "workspace"], ["adopt", "--verbose"]]),
    ("subagents",
     {".claude/agents/reviewer.md": SUBAGENT_CLAUDE,
      ".claude/agents/plain.md": "Just a body with no frontmatter.\n",
      ".copilot/agents/reviewer.agent.md": SUBAGENT_COPILOT,
      ".copilot/agents/helper.agent.md": "---\ndescription: helps\nuser-invocable: false\n---\nHelp body\n",
      ".claude": None},
     [["init", "workspace"], ["adopt", "--dry-run", "--verbose"], ["adopt", "--verbose"], ["adopt", "--verbose"]]),
    ("subagent_invalid",
     {".claude/agents/Bad_Name.md": "---\ndescription: x\n---\nbody\n",
      ".claude/agents/empty-body.md": "---\ndescription: x\n---\n\n"},
     [["init", "workspace"], ["adopt"], ["adopt", "--skip", "subagent/Bad_Name", "--skip", "subagent/empty-body"]]),
    ("skip_unknown", {".claude/CLAUDE.md": CLAUDE_MD},
     [["init", "workspace"], ["adopt", "--skip", "bogus", "--skip", "mcp/nope"]]),
    ("skip_everything", {".claude/CLAUDE.md": CLAUDE_MD},
     [["init", "workspace"], ["adopt", "--skip", "instructions", "--verbose"]]),
    ("no_workspace", {".claude/CLAUDE.md": CLAUDE_MD},
     [["adopt", "--dry-run"], ["adopt"]]),
    ("explicit_target", {".claude/CLAUDE.md": CLAUDE_MD},
     [["init", "workspace"], ["adopt", "H/other-ws"]]),
    ("invalid_agent_file", {".claude/CLAUDE.md": CLAUDE_MD},
     [["init", "workspace"], ["__write__", "aikito/agents/claude-code.toml", "not = [valid"], ["adopt"]]),
    ("bad_args", {".claude": None},
     [["init", "workspace"], ["adopt", "--force"], ["adopt", "a", "b"], ["adopt", "--skip"],
      ["adopt", "--skip", "--dry-run"], ["adopt", "--dry-run=yes"]]),
    ("abbreviated_options", {".claude/CLAUDE.md": CLAUDE_MD},
     [["init", "workspace"], ["adopt", "--dry", "--verb"], ["adopt", "--skip=instructions"],
      ["adopt", "--sk", "instructions", "--verbose"]]),
    ("relative_target", {".claude/CLAUDE.md": CLAUDE_MD},
     [["init", "workspace"], ["adopt", "--verbose", "./rel-ws/"]]),
    ("crlf_instructions",
     {".claude/CLAUDE.md": "# Rules\r\n\r\nLine one   \r\nLine two\r\n",
      ".codex/AGENTS.md": "# Rules\n\nLine one\nLine two\n"},
     [["init", "workspace"], ["adopt", "--verbose"]]),
    ("builtin_mcp",
     {".claude": None,
      ".claude.json": json.dumps({"mcpServers": {"builtin-one": {"command": "b"}, "user-one": {"command": "u"}}})},
     [["init", "workspace"],
      ["__append__", "aikito/agents/claude-code.toml", 'builtin_mcps = ["builtin-one"]\n'],
      ["adopt", "--verbose"], ["adopt", "--skip", "mcp/user-one", "--verbose"]]),
    ("copilot_unsupported_mcp",
     {".claude": None, ".copilot": None,
      ".copilot/mcp-config.json": json.dumps({"mcpServers": {
          "local": {"type": "local", "command": "x"},
          "remote": {"type": "http", "url": "https://r.example/mcp", "headers": {"X-Token": "abc"}}}})},
     [["init", "workspace"], ["adopt", "--verbose"]]),
    ("subagent_frontmatter_forms",
     {".claude": None, ".copilot": None,
      ".copilot/agents/folded.agent.md":
          "---\ndescription: >-\n  Folded line one\n  and two\nmodel: 'gpt-5'\ntools:\n  - read\n  - 'edit'\n"
          "user-invocable: false\n# a comment\n---\n\n  Body with indent\n\n",
      ".copilot/agents/badtools.agent.md": "---\ndescription: x\ntools: [1, 2]\n---\nbody\n",
      ".claude/agents/listdesc.md": "---\ndescription: [a, b]\n---\nbody text\n"},
     [["init", "workspace"], ["adopt", "--verbose"],
      ["adopt", "--skip", "subagent/badtools", "--verbose"]]),
    ("old_layout_workspace", {".claude/CLAUDE.md": CLAUDE_MD, "aikito/agents.toml": "[agents]\n"},
     [["adopt"]]),
]

TS = re.compile(r"adopt_\d{8}_\d{6}")


def normalise(text: str, home: str) -> str:
    return TS.sub("adopt_TS", text.replace(home, "H"))


def snapshot(home: Path) -> dict:
    tree = {}
    for root, dirs, files in os.walk(home):
        rel_root = Path(root).relative_to(home)
        for d in list(dirs):
            rel = (rel_root / d).as_posix()
            p = Path(root) / d
            if rel in ("aikito/.git", "aikito/skills", ".aikito/backups") and not p.is_symlink():
                # backups are listed by file below; .git and bundled skills skipped
                if rel != ".aikito/backups":
                    dirs.remove(d)
                    continue
            if p.is_symlink():
                tree[normalise(rel, str(home))] = "-> " + normalise(os.readlink(p), str(home))
                dirs.remove(d)
            elif not any(p.iterdir()):
                tree[normalise(rel, str(home))] = "<dir>"
        for f in files:
            p = Path(root) / f
            rel = normalise((rel_root / f).as_posix(), str(home))
            if p.is_symlink():
                tree[rel] = "-> " + normalise(os.readlink(p), str(home))
            else:
                with p.open(encoding="utf-8", errors="replace", newline="") as f:
                    tree[rel] = normalise(f.read(), str(home))
    return dict(sorted(tree.items()))


def run_scenario(cli: list[str], name: str, files: dict, steps: list) -> dict:
    home = Path(tempfile.mkdtemp(prefix="adopt-")).resolve()
    try:
        for rel, content in files.items():
            p = home / rel
            if content is None:
                p.mkdir(parents=True, exist_ok=True)
            else:
                p.parent.mkdir(parents=True, exist_ok=True)
                p.write_text(content, encoding="utf-8")
        env = {"HOME": str(home), "PATH": "/usr/bin:/bin", "LANG": "C.UTF-8", "COLUMNS": "80"}
        if "PYTHONPATH" in os.environ and cli[0] == sys.executable:
            env["PYTHONPATH"] = os.environ["PYTHONPATH"]
        results = []
        for step in steps:
            if step[0] in ("__write__", "__append__"):
                p = home / step[1]
                p.parent.mkdir(parents=True, exist_ok=True)
                with p.open("a" if step[0] == "__append__" else "w", encoding="utf-8", newline="") as f:
                    f.write(step[2])
                results.append({"args": step})
                continue
            args = [a.replace("H/", str(home) + "/") for a in step]
            proc = subprocess.run(cli + args, cwd=home, env=env, capture_output=True, text=True)
            if step[:2] == ["init", "workspace"]:
                results.append({"args": step, "exit": proc.returncode})
                continue
            results.append({
                "args": step,
                "stdout": normalise(proc.stdout, str(home)),
                "stderr": normalise(proc.stderr, str(home)),
                "exit": proc.returncode,
            })
        return {"files": files, "steps": results, "tree": snapshot(home)}
    finally:
        shutil.rmtree(home, ignore_errors=True)


def main() -> None:
    if len(sys.argv) == 3 and sys.argv[1] == "--compare":
        binary = str(Path(sys.argv[2]).resolve())
        want = json.loads((HERE / "adopt_vectors.json").read_text(encoding="utf-8"))
        bad = 0
        for name, files, steps in SCENARIOS:
            got = run_scenario([binary], name, files, steps)
            if got != want[name]:
                bad += 1
                print(f"=== {name} differs")
                for i, (g, w) in enumerate(zip(got["steps"], want[name]["steps"])):
                    if g != w and "exit" in w:
                        print(f"--- step {i} {w['args']}")
                        for k in ("stdout", "stderr", "exit"):
                            if g.get(k) != w.get(k):
                                print(f"  [{k}] want:\n{w.get(k)}\n  [{k}] got:\n{g.get(k)}")
                if got["tree"] != want[name]["tree"]:
                    gk, wk = set(got["tree"]), set(want[name]["tree"])
                    for k in sorted(gk | wk):
                        if got["tree"].get(k) != want[name]["tree"].get(k):
                            print(f"  tree {k}:\n    want: {want[name]['tree'].get(k)!r}\n    got:  {got['tree'].get(k)!r}")
        print(f"{bad} scenario(s) differ of {len(SCENARIOS)}")
        sys.exit(1 if bad else 0)

    src = os.environ.get("AIKITO_PYTHON_SRC", "../aikito/src")
    os.environ["PYTHONPATH"] = str(Path(src).resolve())
    out = {}
    for name, files, steps in SCENARIOS:
        out[name] = run_scenario([sys.executable, "-m", "aikito"], name, files, steps)
    (HERE / "adopt_vectors.json").write_text(
        json.dumps(out, indent=1, sort_keys=True, ensure_ascii=False) + "\n", encoding="utf-8"
    )
    print(f"wrote {len(out)} scenarios")


if __name__ == "__main__":
    main()
