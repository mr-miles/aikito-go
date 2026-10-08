"""Generate report_vectors.json: status/doctor/show output from the reference CLI.

Run from the repo root (reference checkout at ../aikito):
    python3 internal/cli/testdata/gen_report_vectors.py
Compare a Go binary against the reference instead of writing vectors:
    python3 internal/cli/testdata/gen_report_vectors.py --compare ./aikito [scenario...]

Each scenario is a list of setup operations applied to a fresh HOME, then a
list of read-only report commands. Setup "cli" steps are replayed with the
CLI under test (the Go test replays them in-process); every other step is a
plain filesystem operation. "{H}" in arguments and file contents is the HOME
path; the HOME path is written as "H" in captured output.
"""
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
PY_SRC = (ROOT.parent / "aikito" / "src").resolve()
if not PY_SRC.is_dir():
    # Worktrees live under <repo>/.claude/worktrees/<name>.
    PY_SRC = Path("/home/miles/aikito-rs/aikito/src")

NOTE = """---
title: {title}
description: {desc}
updated: 2026-01-01
---

{body}
"""

SKILL = """---
name: {name}
description: {desc}
---

# {name}

Body.
"""

BASE = [
    ["mkdir", ".claude"],
    ["mkdir", ".codex"],
    ["cli", "init", "workspace"],
]
SYNCED = BASE + [["cli", "sync", "global"]]

PROJECT_TOML = 'name = "{name}"\npath = "{path}"\nsync_mode = "{mode}"\nskills = {skills}\n'


def project(name, path, skills="[]", mode="link"):
    return [
        ["mkdir", f"aikito/projects/{name}/memory/notes"],
        ["write", f"aikito/projects/{name}/agent.toml", PROJECT_TOML.format(name=name, path=path, skills=skills, mode=mode)],
        ["write", f"aikito/projects/{name}/AGENTS.md", f"# {name}\n\nProject rules.\n"],
    ]


# Fixed modification times keep dates in the output stable.
STAMP = 1577880000  # 2020-01-01 12:00 UTC


def note(path, title, body, stamp=STAMP):
    text = (f"# {title}\n\n" if title else "") + body + "\n"
    return [["write", path, text], ["mtime", path, str(stamp)]]


SCENARIOS = {
    "fresh": BASE,
    "synced": SYNCED,
    "failed_global_sync": [
        ["mkdir", ".claude"],
        ["mkdir", ".codex"],
        ["write", ".claude/CLAUDE.md", "# Hand written\n"],
        ["cli", "init", "workspace"],
        ["cli", "sync", "global"],
    ],
    "prepopulated_claude_skills": [
        ["mkdir", ".codex"],
        ["write", ".claude/skills/mine/SKILL.md", SKILL.format(name="mine", desc="Mine")],
        ["cli", "init", "workspace"],
        ["cli", "sync", "global"],
    ],
    "stale_hub_entry": SYNCED + [
        ["write", "aikito/skills/extra/SKILL.md", SKILL.format(name="extra", desc="Extra skill")],
        ["write", "aikito/skills.toml", 'skills = ["aikito", "durable-memory", "extra"]\n'],
        ["cli", "sync", "global"],
        ["rm", "aikito/skills/extra"],
        ["write", "aikito/skills.toml", 'skills = ["aikito", "durable-memory"]\n'],
    ],
    "custom_skill": SYNCED + [
        ["write", "aikito/skills/writer/SKILL.md", SKILL.format(name="writer", desc="Writes things")],
        ["write", "aikito/skills.toml", 'skills = ["aikito", "durable-memory", "writer"]\n'],
    ],
    "projects": SYNCED + [
        ["mkdir", "code/alpha"],
        *project("alpha", "~/code/alpha"),
        *project("ghost", "~/code/ghost"),
    ],
    "memory_notes": SYNCED + [
        *note("aikito/memory/notes/simplified-clean.md", "Simplified clean", "Prefer small functions."),
        *note("aikito/memory/notes/old-thing.md", "", "Old."),
    ],
    "project_synced": SYNCED + [
        ["mkdir", "code/alpha"],
        ["write", "aikito/skills/writer/SKILL.md", SKILL.format(name="writer", desc="Writes things")],
        *project("alpha", "~/code/alpha", '["writer"]'),
        *note("aikito/projects/alpha/memory/notes/alpha-fact.md", "Alpha fact", "Fact."),
        ["cli", "sync", "project", "alpha"],
    ],
    "project_copy_drift": SYNCED + [
        ["mkdir", "code/alpha"],
        ["write", "aikito/skills/writer/SKILL.md", SKILL.format(name="writer", desc="Writes things")],
        *project("alpha", "~/code/alpha", '["writer"]', mode="copy"),
        ["cli", "sync", "project", "alpha"],
        ["write", "code/alpha/.agents/skills/writer/SKILL.md", "edited by hand\n"],
    ],
    "prefixes": SYNCED + [
        ["mkdir", "code/alpha"],
        *project("alpha", "~/code/alpha"),
        *project("alpine", "~/code/alpine"),
        ["write", "aikito/skills/alpha-one/SKILL.md", SKILL.format(name="alpha-one", desc="One")],
        ["write", "aikito/skills/alpha-two/SKILL.md", SKILL.format(name="alpha-two", desc="Two")],
        ["write", "aikito/skills.toml", 'skills = ["aikito", "durable-memory", "alpha-one"]\n'],
        *note("aikito/memory/notes/dup.md", "Global dup", "G."),
        *note("aikito/projects/alpha/memory/notes/dup.md", "Alpha dup", "A."),
    ],
    "inbox": BASE + [
        *note("aikito/inbox/a-note.md", "A note", "Hello."),
        *note("aikito/inbox/sub/b-note.md", "B note", "Nested.", stamp=1600000000),
        *note("aikito/inbox/.hidden.md", "Hidden", "No."),
    ],
    "mcp_synced": SYNCED + [
        ["cli", "add", "mcp", "fetcher", "--transport", "remote", "--url", "https://example.com/mcp", "--agents", "claude-code,codex"],
        ["cli", "sync", "mcp"],
    ],
    "mcp_drifted": SYNCED + [
        ["cli", "add", "mcp", "fetcher", "--transport", "remote", "--url", "https://example.com/mcp", "--agents", "claude-code,codex"],
        ["cli", "sync", "mcp"],
        ["edit_json", ".claude.json", "mcpServers.fetcher.url", "https://example.com/other"],
    ],
    "subagent": SYNCED + [
        ["cli", "add", "subagent", "verifier", "--description", "Verifies work"],
        ["cli", "sync", "subagents"],
    ],
}

COMMON = [
    ["status"],
    ["doctor"],
    ["doctor", "--json"],
    ["show", "skills"],
    ["show", "mcp"],
    ["show", "subagents"],
    ["show", "projects"],
    ["show", "memory"],
    ["show", "inbox"],
    ["show", "instructions"],
]
# A command whose first element is "@cwd=<dir>" runs from HOME/<dir>.
EXTRA = {
    "fresh": [
        ["status", "--color", "always"], ["doctor", "--color", "always"],
        ["show", "skill", "dur"], ["show", "skill", "nosuch"], ["show", "instructions", "global"],
        ["show", "instructions", "nosuch"], ["show", "mcp", "x"], ["show", "subagent", "x"],
        ["show", "project", "x"], ["status", "--bogus"],
    ],
    "custom_skill": [["show", "skill", "writer"], ["show", "skills", "--no-color"], ["show", "skills", "--color", "always"]],
    "projects": [
        ["show", "project", "alpha"], ["show", "project", "ghost"], ["show", "instructions", "alpha"],
        ["@cwd=code/alpha", "show", "project"], ["@cwd=code/alpha", "show", "project", "."],
        ["@cwd=code/alpha", "show", "instructions", "."], ["@cwd=code/alpha", "show", "memory"],
        ["@cwd=code", "show", "project", "."],
    ],
    "memory_notes": [["show", "memory", "simplified"], ["show", "memory", "--all"], ["show", "memory", "nosuch"]],
    "project_synced": [
        ["show", "project", "alpha"], ["show", "memory", "--all"], ["show", "memory", "--project", "alpha"],
        ["show", "memory", "alpha-fact"], ["show", "skills", "--color", "always"],
    ],
    "project_copy_drift": [["show", "project", "alpha"]],
    "prefixes": [
        ["show", "project", "al"], ["show", "project", "zz"], ["show", "skill", "alpha"],
        ["show", "memory", "--all"], ["show", "memory", "dup"], ["show", "memory", "global/dup"],
        ["show", "memory", "--project", "al", "dup"], ["show", "memory", "--project", "alph", "dup"],
        ["show", "memory", "--all", "--project", "alpha"],
    ],
    "inbox": [
        ["show", "inbox", "a"], ["show", "inbox", "sub/b-note"], ["show", "inbox", "b-note.md"],
        ["show", "inbox", "zz"], ["show", "inbox", "--color", "always"],
    ],
    "mcp_synced": [["show", "mcp", "fetcher"], ["show", "mcps"], ["show", "mcp", "--color", "always"]],
    "mcp_drifted": [["show", "mcp", "fetcher"], ["show", "mcp", "--color", "always"]],
    "subagent": [["show", "subagent", "verifier"]],
}


def apply_setup(home: Path, steps, cli):
    for step in steps:
        op, args = step[0], [a.replace("{H}", str(home)) for a in step[1:]]
        if op == "cli":
            run(cli, home, args)
        elif op == "mkdir":
            (home / args[0]).mkdir(parents=True, exist_ok=True)
        elif op == "write":
            p = home / args[0]
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text(args[1], encoding="utf-8")
        elif op == "rm":
            p = home / args[0]
            if p.is_dir() and not p.is_symlink():
                shutil.rmtree(p)
            else:
                p.unlink()
        elif op == "mtime":
            os.utime(home / args[0], (int(args[1]), int(args[1])))
        elif op == "edit_json":
            p = home / args[0]
            doc = json.loads(p.read_text())
            cur = doc
            keys = args[1].split(".")
            for k in keys[:-1]:
                cur = cur[k]
            cur[keys[-1]] = args[2]
            p.write_text(json.dumps(doc, indent=2) + "\n")
        else:
            raise SystemExit(f"unknown op {op}")


def env_for(home: Path):
    return {
        "HOME": str(home),
        "PATH": "/usr/bin:/bin",
        "LANG": "C.UTF-8",
        "COLUMNS": "80",
        "PYTHONPATH": str(PY_SRC),
        "GIT_CONFIG_NOSYSTEM": "1",
        "TZ": "UTC",
    }


def run(cli, home: Path, args):
    cwd = home
    if args and args[0].startswith("@cwd="):
        cwd, args = home / args[0][len("@cwd="):], args[1:]
    p = subprocess.run(cli + args, cwd=cwd, env=env_for(home), capture_output=True, text=True)
    norm = lambda s: s.replace(str(home), "H")
    return {"stdout": norm(p.stdout), "stderr": norm(p.stderr), "exit": p.returncode}


PY_CLI = [sys.executable, "-m", "aikito"]


def drop_python_only(result):
    """Remove doctor's interpreter-consistency finding, which only the
    Python CLI can produce (the Go test applies the same filter)."""
    out = result["stdout"]
    if '"message": "Interpreter' in out:
        doc = json.loads(out)
        for section in doc["sections"]:
            section["findings"] = [
                f for f in section["findings"] if not f["message"].startswith("Interpreter")
            ]
        out = json.dumps(doc, ensure_ascii=False, indent=2) + "\n"
    else:
        ansi = re.compile(r"\x1b\[[0-9;]*m")
        out = "".join(
            line for line in out.splitlines(keepends=True)
            if not ansi.sub("", line).startswith("  ✓ Interpreter")
        )
    return {**result, "stdout": out}


def capture(name, cli):
    home = Path(tempfile.mkdtemp(prefix="aikrep-")).resolve()
    try:
        apply_setup(home, SCENARIOS[name], PY_CLI)
        return [{"args": c, **drop_python_only(run(cli, home, c))} for c in COMMON + EXTRA.get(name, [])]
    finally:
        shutil.rmtree(home, ignore_errors=True)


def main():
    if len(sys.argv) > 2 and sys.argv[1] == "--compare":
        go = [str(Path(sys.argv[2]).resolve())]
        names = sys.argv[3:] or list(SCENARIOS)
        bad = 0
        for name in names:
            home = Path(tempfile.mkdtemp(prefix="aikrep-")).resolve()
            apply_setup(home, SCENARIOS[name], PY_CLI)
            for c in COMMON + EXTRA.get(name, []):
                want, got = drop_python_only(run(PY_CLI, home, c)), run(go, home, c)
                if want != got:
                    bad += 1
                    print(f"##### {name}: aikito {' '.join(c)}")
                    for k in ("exit", "stdout", "stderr"):
                        if want[k] != got[k]:
                            print(f"--- {k} (python)\n{want[k]}\n+++ {k} (go)\n{got[k]}")
            shutil.rmtree(home, ignore_errors=True)
        print(f"{bad} differing command(s)")
        return
    out = {}
    for name in SCENARIOS:
        out[name] = {"setup": SCENARIOS[name], "commands": capture(name, PY_CLI)}
    Path(__file__).with_name("report_vectors.json").write_text(
        json.dumps(out, indent=1, sort_keys=True, ensure_ascii=False) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
