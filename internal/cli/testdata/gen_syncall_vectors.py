"""Generate internal/cli/testdata/syncall_vectors.json from the real Python CLI.

Scenarios for the whole-workspace `aikito sync` (cmd_sync_all), in the same
step format as gen_syncglobal_vectors.py (whose tree snapshot, path
normalisation and CLI runner this reuses), plus:

  {"op": "replace", "path", "old", "new"}  edit a file in place
  {"op": "run", ..., "env": {...}}         extra environment ("H/..." = home)

Workspaces are set up with `init workspace` and plain file writes (contents
copied from what the reference `add`/`init project` commands write), so the
Go replay (syncall_test.go) doesn't depend on other commands.

Requires python3 and a reference checkout (AIKITO_PYTHON_SRC, else a sibling
../aikito/src next to this repo). Run from anywhere:

    python3 internal/cli/testdata/gen_syncall_vectors.py
"""
import json
import os
import shutil
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from gen_syncglobal_vectors import HERE, SRC, normalize, tree  # noqa: E402

GSKILL = "---\nname: {0}\ndescription: A {0} skill\n---\n\n# {0}\n"
MCP_REMOTE = 'transport = "remote"\nurl = "https://example.com/mcp"\nagents = ["claude-code", "codex"]\n'
MCP_STDIO = 'command = "npx"\nargs = []\nagents = ["claude-code", "codex"]\n'
SUBAGENT = ('---\ndescription: "Verifies things"\nagents: [{0}]\n---\n# Verifier\n\n'
            "Add developer instructions for the verifier subagent here.\n")


def w(path, content):
    return {"op": "write", "path": path, "content": content}


def project(name, sync_mode="link", skills=("pskill",), checkout=True):
    body = ", ".join(f'"{s}"' for s in skills)
    steps = [
        w(f"aikito/projects/{name}/agent.toml",
          f'name = "{name}"\npath = "~/{name}"\nsync_mode = "{sync_mode}"\nskills = [{body}]\n'),
        w(f"aikito/projects/{name}/AGENTS.md", ""),
        {"op": "mkdir", "path": f"aikito/projects/{name}/memory/notes"},
    ]
    if checkout:
        steps.append({"op": "mkdir", "path": name})
    return steps


def init(*dirs):
    return [{"op": "mkdir", "path": d} for d in dirs] + [{"op": "init"}]


def run(*args, env=None):
    step = {"op": "run", "args": ["sync", *args]}
    if env:
        step["env"] = env
    return step


SYNC, DRY, VERBOSE, DRYV = run(), run("--dry-run"), run("--verbose"), run("--dry-run", "--verbose")
TREE = {"op": "tree"}

RICH = (
    init(".claude", ".codex")
    + [w("aikito/skills/gskill/SKILL.md", GSKILL.format("gskill")),
       w("aikito/skills/pskill/SKILL.md", GSKILL.format("pskill")),
       w("aikito/skills.toml", 'skills = ["aikito", "durable-memory", "gskill"]\n'),
       w("aikito/mcps/remote-s.toml", MCP_REMOTE),
       w("aikito/subagents/verifier.md", SUBAGENT.format('"claude-code"'))]
    + project("p1") + project("p2", sync_mode="copy") + project("p3", checkout=False)
    + [w("aikito/projects/p4/agent.toml", 'name = "p4"\nskills = []\n'),
       {"op": "mkdir", "path": "aikito/projects/.hidden"},
       {"op": "mkdir", "path": "aikito/projects/noconfig"}]
)
SYNCED = RICH + [SYNC]

SCENARIOS = {
    "fresh_claude_codex": init(".claude", ".codex") + [DRY, DRYV, SYNC, SYNC, VERBOSE, TREE],
    "rich": RICH + [DRYV, SYNC, TREE,
                    {"op": "read", "path": ".claude.json"},
                    {"op": "read", "path": ".codex/config.toml"},
                    {"op": "read", "path": "p2/.agents/skills/pskill/SKILL.md"},
                    VERBOSE],
    "prepopulated_claude_skills": SYNCED
    + [{"op": "rm", "path": ".claude/skills"}, w(".claude/skills/mine/SKILL.md", "hi\n"), DRY, VERBOSE, TREE],
    "unmanaged_claude_md": SYNCED + [{"op": "rm", "path": ".claude/CLAUDE.md"}, w(".claude/CLAUDE.md", "# mine\n"),
                                     DRYV, SYNC, TREE],
    "drifted_mcp_config": SYNCED
    + [{"op": "replace", "path": ".claude.json", "old": "https://example.com/mcp", "new": "https://changed.example.com/mcp"},
       DRYV, SYNC],
    "drifted_copied_skill": SYNCED
    + [{"op": "append", "path": "p2/.agents/skills/pskill/SKILL.md", "content": "local edit\n"},
       {"op": "append", "path": "aikito/skills/pskill/SKILL.md", "content": "canonical edit\n"}, DRYV, SYNC],
    "canonical_changes_applied": SYNCED
    + [{"op": "append", "path": "aikito/skills/pskill/SKILL.md", "content": "canonical edit\n"},
       {"op": "append", "path": "aikito/subagents/verifier.md", "content": "more\n"}, DRYV, SYNC, TREE,
       {"op": "read", "path": "p2/.agents/skills/pskill/SKILL.md"},
       {"op": "read", "path": ".claude/agents/verifier.md"}],
    "missing_project_skill_source": RICH
    + [w("aikito/projects/p1/agent.toml",
         'name = "p1"\npath = "~/p1"\nsync_mode = "link"\nskills = ["pskill", "missing"]\n'), DRYV, SYNC],
    "orphaned_subagent": SYNCED + [{"op": "rm", "path": "aikito/subagents/verifier.md"}, DRYV, SYNC, TREE],
    "stale_hub_entry": SYNCED + [w("aikito/skills.toml", 'skills = ["aikito", "durable-memory"]\n'), DRYV, SYNC, TREE],
    "checkout_goes_offline": SYNCED + [{"op": "rm", "path": "p1"}, VERBOSE],
    "skills_toml_conflict_marker": SYNCED + [{"op": "append", "path": "aikito/skills.toml", "content": "<<<<<<< HEAD\n"}, VERBOSE],
    "skills_not_a_list": SYNCED + [w("aikito/skills.toml", 'skills = "x"\n'), VERBOSE],
    "missing_skills_toml": SYNCED + [{"op": "rm", "path": "aikito/skills.toml"}, VERBOSE],
    "missing_global_instructions": SYNCED + [{"op": "rm", "path": "aikito/global/AGENTS.md"}, VERBOSE],
    "subagent_config_error": RICH + [w("aikito/subagents/verifier.md", SUBAGENT.format('"agy"')), DRYV, SYNC],
    "mcp_config_error": RICH + [w("aikito/mcps/stdio-s.toml", MCP_STDIO), DRYV, SYNC],
    "bundled_drift_no_projects": init(".claude") + [SYNC, {"op": "append", "path": "aikito/skills/aikito/SKILL.md",
                                                            "content": "tweak\n"}, DRYV, SYNC, SYNC, TREE],
    "bundled_drift_replan": SYNCED
    + [{"op": "append", "path": "aikito/skills/durable-memory/SKILL.md", "content": "tweak\n"},
       {"op": "rm", "path": "aikito/skills/aikito"}, DRYV, SYNC, SYNC, TREE],
    "agents_dir_env": init(".claude", "altagents") + [run("--verbose", env={"AIKITO_AGENTS_DIR": "H/altagents"}), TREE],
    "no_workspace": [{"op": "mkdir", "path": ".claude"}, SYNC, DRYV],
    # `sync subagents` shares the subagent executor and planner messages.
    "subagents_update_backup": init(".claude")
    + [w("aikito/subagents/verifier.md", SUBAGENT.format('"claude-code"')), run("subagents"),
       {"op": "append", "path": "aikito/subagents/verifier.md", "content": "more\n"}, run("subagents"), TREE,
       {"op": "read", "path": ".claude/agents/verifier.md"}],
    "subagents_orphan": init(".claude")
    + [w("aikito/subagents/verifier.md", SUBAGENT.format('"claude-code"')), run("subagents"),
       {"op": "rm", "path": "aikito/subagents/verifier.md"}, run("subagents"), run("subagents", "--prune"), TREE],
    "parent_flags": init(".claude")
    + [run("--dry-run", "global"), run("--dry", "--verb"), run("--dry-run", "--dry-run"), run("--bogus"),
       run("extra"), run("--dry-run", "project", "nosuch"), run("--verbose", "global"), TREE],
}


def run_cli(args, home, extra_env=None):
    import subprocess
    env = {"HOME": home, "PATH": "/usr/bin:/bin", "PYTHONPATH": os.path.abspath(SRC),
           "COLUMNS": "80", "LANG": "C.UTF-8"}
    for k, v in (extra_env or {}).items():
        env[k] = home + v[1:] if v.startswith("H/") else v
    return subprocess.run(["python3", "-m", "aikito", *args], cwd=home, env=env, capture_output=True, text=True)


def run_scenario(steps):
    home = os.path.realpath(tempfile.mkdtemp())
    out = []
    try:
        for step in steps:
            op = step["op"]
            path = os.path.join(home, step.get("path", ""))
            if op == "mkdir":
                os.makedirs(path, exist_ok=True)
            elif op in ("write", "append"):
                os.makedirs(os.path.dirname(path), exist_ok=True)
                with open(path, "a" if op == "append" else "w") as f:
                    f.write(step["content"])
            elif op == "replace":
                text = Path(path).read_text()
                assert step["old"] in text, (path, step["old"])
                Path(path).write_text(text.replace(step["old"], step["new"]))
            elif op == "rm":
                if os.path.isdir(path) and not os.path.islink(path):
                    shutil.rmtree(path)
                else:
                    os.remove(path)
            elif op == "init":
                proc = run_cli(["init", "workspace"], home)
                assert proc.returncode == 0, proc.stderr
            elif op == "run":
                proc = run_cli(step["args"], home, step.get("env"))
                out.append({"args": step["args"], "exit": proc.returncode,
                            "stdout": normalize(proc.stdout, home), "stderr": normalize(proc.stderr, home)})
            elif op == "tree":
                out.append({"tree": tree(home)})
            elif op == "read":
                out.append({"read": step["path"], "content": normalize(Path(path).read_text(), home)})
            else:
                raise ValueError(op)
    finally:
        shutil.rmtree(home, ignore_errors=True)
    return out


def main():
    vectors = {name: {"steps": steps, "expect": run_scenario(steps)} for name, steps in SCENARIOS.items()}
    with open(os.path.join(HERE, "syncall_vectors.json"), "w") as f:
        json.dump(vectors, f, indent=1, sort_keys=True, ensure_ascii=False)
        f.write("\n")


if __name__ == "__main__":
    main()
