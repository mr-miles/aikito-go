"""Generate internal/cli/testdata/syncglobal_vectors.json from the real Python CLI.

Each scenario is a list of steps run against a fresh HOME: filesystem setup,
`aikito init workspace`, and `aikito sync global` runs whose stdout, stderr
and exit code are recorded, plus snapshots of the resulting home tree.
syncglobal_test.go replays the same steps against the Go CLI and compares.

Paths are written with the temp HOME replaced by "H". Tree snapshots skip
the workspace itself (<home>/aikito, whose bundled-skill file modes depend on
the reference checkout's permissions) and record modes only under ~/.local
(the writer-lock state store, where Python sets 0700/0600).

Requires python3 and a reference checkout (AIKITO_PYTHON_SRC, else a sibling
../aikito/src next to this repo). Run from anywhere:

    python3 internal/cli/testdata/gen_syncglobal_vectors.py
"""
import json
import os
import re
import shutil
import subprocess
import tempfile
from pathlib import Path

HERE = os.path.dirname(os.path.abspath(__file__))
SRC = os.environ.get("AIKITO_PYTHON_SRC") or os.path.join(HERE, "..", "..", "..", "..", "aikito", "src")

SKILL_MD = "---\nname: {0}\ndescription: test\n---\nbody\n"


def mkskill(name):
    return [{"op": "write", "path": f"aikito/skills/{name}/SKILL.md", "content": SKILL_MD.format(name)}]


def setskills(*names):
    body = ", ".join(f'"{n}"' for n in names)
    return {"op": "write", "path": "aikito/skills.toml", "content": f"skills = [{body}]\n"}


def init(*dirs):
    return [{"op": "mkdir", "path": d} for d in dirs] + [{"op": "init"}]


SYNC = {"op": "run", "args": ["sync", "global"]}
DRY = {"op": "run", "args": ["sync", "global", "--dry-run"]}
TREE = {"op": "tree"}
ALL_MARKERS = [".claude", ".codex", ".gemini", ".config/opencode", ".copilot", ".grok", ".pi", ".dsh", ".deepseek"]

SCENARIOS = {
    "claude_only": init(".claude") + [DRY, SYNC, SYNC, TREE],
    "claude_codex": init(".claude", ".codex") + [DRY, SYNC, SYNC, TREE],
    "all_markers": init(*ALL_MARKERS) + [DRY, SYNC, DRY, SYNC, TREE],
    "no_markers": init() + [DRY, SYNC, TREE],
    "prepopulated_claude_skills": init(".claude/skills/mine")
    + [{"op": "write", "path": ".claude/skills/mine/SKILL.md", "content": "hi\n"}, DRY, SYNC, TREE],
    "claude_skills_symlink_elsewhere": init(".claude", "other/skills")
    + [{"op": "symlink", "path": ".claude/skills", "target": "H/other/skills"}, DRY, SYNC, TREE],
    "stale_hub_entry": init(".claude", ".codex") + mkskill("foo")
    + [setskills("aikito", "durable-memory", "foo"), SYNC, setskills("aikito", "durable-memory"), DRY, SYNC, SYNC, TREE],
    "broken_stale_hub_entry": init(".claude") + mkskill("foo")
    + [setskills("aikito", "durable-memory", "foo"), SYNC, setskills("aikito", "durable-memory"),
       {"op": "rm", "path": "aikito/skills/foo"}, SYNC, TREE],
    "foreign_hub_entries": init(".claude")
    + [{"op": "mkdir", "path": ".agents/skills/mine"}, {"op": "mkdir", "path": "elsewhere"},
       {"op": "symlink", "path": ".agents/skills/ext", "target": "H/elsewhere"}, DRY, SYNC, TREE],
    "unmanaged_claude_md": init(".claude", ".codex")
    + [{"op": "write", "path": ".claude/CLAUDE.md", "content": "mine\n"}, DRY, SYNC, TREE],
    "old_go_layout": init(".claude")
    + [{"op": "mkdir", "path": ".claude/skills"},
       {"op": "symlink", "path": ".claude/skills/aikito", "target": "H/aikito/skills/aikito"},
       {"op": "symlink", "path": ".claude/skills/durable-memory", "target": "H/aikito/skills/durable-memory"},
       {"op": "symlink", "path": ".claude/CLAUDE.md", "target": "H/aikito/global/AGENTS.md"}, DRY, SYNC, TREE],
    "legacy_container_symlink": init(".claude")
    + [{"op": "mkdir", "path": ".agents"}, {"op": "symlink", "path": ".agents/skills", "target": "H/aikito/skills"},
       DRY, SYNC, SYNC, TREE],
    "container_symlink_elsewhere": init(".claude")
    + [{"op": "mkdir", "path": ".agents"}, {"op": "symlink", "path": ".agents/skills", "target": "H/elsewhere"}, SYNC, TREE],
    "container_symlink_subpath": init(".claude")
    + [{"op": "mkdir", "path": ".agents"}, {"op": "symlink", "path": ".agents/skills", "target": "H/aikito/skills/aikito"},
       SYNC, TREE],
    "container_is_file": init(".claude") + [{"op": "write", "path": ".agents/skills", "content": "x\n"}, SYNC, TREE],
    "consumer_is_file": init(".claude") + [{"op": "write", "path": ".claude/skills", "content": "x\n"}, SYNC, TREE],
    "missing_canonical_skill": init(".claude") + [setskills("aikito", "nope"), DRY, SYNC, TREE],
    "dangling_selected_entry": init(".claude") + mkskill("foo")
    + [setskills("aikito", "durable-memory", "foo"), SYNC, {"op": "rm", "path": "aikito/skills/foo"}, SYNC, TREE],
    "missing_global_instructions": init(".claude") + [{"op": "rm", "path": "aikito/global/AGENTS.md"}, DRY, SYNC, TREE],
    "instruction_target_is_dir": init(".claude") + [{"op": "mkdir", "path": ".claude/CLAUDE.md"}, SYNC, TREE],
    "instruction_symlink_elsewhere": init(".claude")
    + [{"op": "symlink", "path": ".claude/CLAUDE.md", "target": "H/elsewhere.md"}, SYNC, TREE],
    "grok_legacy_link": init(".grok")
    + [{"op": "symlink", "path": ".grok/AGENTS.md", "target": "H/aikito/global/AGENTS.md"}, DRY, SYNC, TREE],
    "grok_legacy_file": init(".grok") + [{"op": "write", "path": ".grok/AGENTS.md", "content": "mine\n"}, SYNC, TREE],
    "agent_marker_removed": init(".claude", ".codex") + [{"op": "rm", "path": ".codex"}, DRY, SYNC, TREE],
    "custom_agent_shares_claude_paths": init(".claude")
    + [{"op": "write", "path": "aikito/agents/zz-custom.toml",
        "content": '[agents.zz-custom]\ndisplay_name = "ZZ Custom"\nskills_path = ".claude/skills"\n'
                   'instruction_path = ".claude/CLAUDE.md"\n'}, SYNC, SYNC, TREE],
    "relative_hub_link": init(".claude")
    + [{"op": "mkdir", "path": ".agents/skills"},
       {"op": "symlink", "path": ".agents/skills/aikito", "target": "../../aikito/skills/aikito"}, SYNC, TREE],
    "bundled_skill_drift": init(".claude")
    + [{"op": "append", "path": "aikito/skills/aikito/SKILL.md", "content": "extra\n"}, DRY, SYNC, TREE,
       {"op": "read", "path": "aikito/skills/aikito/SKILL.md"}],
    "bundled_skill_missing": init(".claude") + [{"op": "rm", "path": "aikito/skills/durable-memory"}, DRY, SYNC, TREE],
    "missing_skills_toml": init(".claude") + [{"op": "rm", "path": "aikito/skills.toml"}, SYNC],
    "skills_not_a_list": init(".claude") + [{"op": "write", "path": "aikito/skills.toml", "content": 'skills = "x"\n'}, SYNC],
    "no_skills_key": init(".claude") + [{"op": "write", "path": "aikito/skills.toml", "content": "x = 1\n"}, SYNC, TREE],
    "skills_toml_conflict_marker": init(".claude")
    + [{"op": "write", "path": "aikito/skills.toml", "content": 'skills = ["aikito"]\n<<<<<<< HEAD\n'}, SYNC],
    "skill_conflict_marker": init(".claude") + mkskill("foo")
    + [setskills("foo"), {"op": "append", "path": "aikito/skills/foo/SKILL.md",
                          "content": "<<<<<<< a\nx\n=======\ny\n>>>>>>> b\n"}, SYNC],
    "no_workspace": [SYNC],
    "old_layout": init(".claude") + [{"op": "rm", "path": "aikito/layout.toml"},
                                     {"op": "write", "path": "aikito/agents.toml", "content": ""}, SYNC],
    "unrecognized_arguments": init(".claude")
    + [{"op": "run", "args": ["sync", "global", "--force"]}, {"op": "run", "args": ["sync", "global", "foo", "--dry-run"]}],
}

BACKUP_TS = re.compile(r"bundled-skills_\d{8}_\d{6}_\d{6}")


def normalize(text, home):
    return BACKUP_TS.sub("bundled-skills_TS", text.replace(home, "H"))


def tree(home):
    lines = []
    for root, dirs, files in os.walk(home):
        dirs.sort()
        for name in sorted(dirs + files):
            p = os.path.join(root, name)
            rel = BACKUP_TS.sub("bundled-skills_TS", os.path.relpath(p, home))
            if rel == "aikito" or rel.startswith("aikito" + os.sep):
                continue
            mode = ""
            if rel.startswith(".local"):
                mode = " " + oct(os.lstat(p).st_mode & 0o777)[2:]
            if os.path.islink(p):
                lines.append(f"L {rel} -> {normalize(os.readlink(p), home)}")
            elif os.path.isdir(p):
                lines.append(f"D {rel}{mode}")
            else:
                lines.append(f"F {rel}{mode}")
        dirs[:] = [d for d in dirs if not os.path.islink(os.path.join(root, d))]
    return sorted(lines)


def run_cli(args, home):
    env = {"HOME": home, "PATH": "/usr/bin:/bin", "PYTHONPATH": os.path.abspath(SRC),
           "COLUMNS": "80", "LANG": "C.UTF-8"}
    proc = subprocess.run(["python3", "-m", "aikito", *args], cwd=home, env=env, capture_output=True, text=True)
    return proc


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
            elif op == "symlink":
                os.symlink(step["target"].replace("H", home, 1) if step["target"].startswith("H/") else step["target"], path)
            elif op == "rm":
                if os.path.isdir(path) and not os.path.islink(path):
                    shutil.rmtree(path)
                else:
                    os.remove(path)
            elif op == "init":
                proc = run_cli(["init", "workspace"], home)
                assert proc.returncode == 0, proc.stderr
            elif op == "run":
                proc = run_cli(step["args"], home)
                out.append({"args": step["args"], "exit": proc.returncode,
                            "stdout": normalize(proc.stdout, home), "stderr": normalize(proc.stderr, home)})
            elif op == "tree":
                out.append({"tree": tree(home)})
            elif op == "read":
                out.append({"read": step["path"], "content": Path(path).read_text()})
    finally:
        shutil.rmtree(home, ignore_errors=True)
    return out


def main():
    vectors = {name: {"steps": steps, "expect": run_scenario(steps)} for name, steps in SCENARIOS.items()}
    with open(os.path.join(HERE, "syncglobal_vectors.json"), "w") as f:
        json.dump(vectors, f, indent=1, sort_keys=True, ensure_ascii=False)
        f.write("\n")


if __name__ == "__main__":
    main()
