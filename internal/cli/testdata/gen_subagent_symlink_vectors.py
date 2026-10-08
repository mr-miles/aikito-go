"""Generate subagent_symlink_vectors.json from the reference CLI.

Run from the repo root (AIKITO_PYTHON_SRC defaults to ../aikito/src):
    python3 internal/cli/testdata/gen_subagent_symlink_vectors.py

Subagent files that are symlinks are listed like regular files (Python's
Path.is_file() follows links): an unmanaged one is adopted, a managed one
is reported as an orphan and pruned. Steps are ["run", args...] or file ops;
HOME is written as {H} and backup timestamps as {TS}.
"""
import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
PYSRC = Path(os.environ.get("AIKITO_PYTHON_SRC", ROOT.parent / "aikito" / "src")).resolve()

HELPER = '---\nname: helper\ndescription: "Helps out"\n---\nHelp with the task.\n'
OLD_DEF = '---\ndescription: "An old subagent"\nagents: ["claude-code"]\n---\nDo old things.\n'

SCENARIOS = {
    "adopt_symlinked_unmanaged": [
        ["mkdir", ".claude/agents"],
        ["write", "elsewhere/helper.md", HELPER],
        ["symlink", ".claude/agents/helper.md", "elsewhere/helper.md"],
        ["run", "init", "workspace"],
        ["run", "adopt"],
        ["read", "aikito/subagents/helper.md"],
    ],
    "orphan_symlinked_managed": [
        ["mkdir", ".claude"],
        ["run", "init", "workspace"],
        ["write", "aikito/subagents/old.md", OLD_DEF],
        ["run", "sync", "subagents"],
        ["move", ".claude/agents/old.md", "elsewhere/old.md"],
        ["symlink", ".claude/agents/old.md", "elsewhere/old.md"],
        ["remove", "aikito/subagents/old.md"],
        ["run", "sync", "subagents"],
        ["run", "sync", "subagents", "--prune"],
        ["exists", ".claude/agents/old.md"],
        ["exists", "elsewhere/old.md"],
    ],
}


def norm(text, home):
    text = text.replace(str(home), "{H}")
    text = re.sub(r"\d{8}T\d{12}Z", "{TS}", text)
    return re.sub(r"adopt_\d{8}_\d{6}", "adopt_{TS}", text)


out = {}
for name, steps in SCENARIOS.items():
    home = Path(tempfile.mkdtemp()).resolve()
    env = {"HOME": str(home), "PATH": "/usr/bin:/bin", "PYTHONPATH": str(PYSRC)}
    results = []
    for st in steps:
        op = st[0]
        if op == "mkdir":
            (home / st[1]).mkdir(parents=True, exist_ok=True)
        elif op == "write":
            (home / st[1]).parent.mkdir(parents=True, exist_ok=True)
            (home / st[1]).write_text(st[2])
        elif op == "symlink":
            (home / st[1]).symlink_to(home / st[2])
        elif op == "move":
            (home / st[2]).parent.mkdir(parents=True, exist_ok=True)
            (home / st[1]).rename(home / st[2])
        elif op == "remove":
            (home / st[1]).unlink()
        elif op == "run":
            p = subprocess.run([sys.executable, "-m", "aikito", *st[1:]], cwd=home, env=env, capture_output=True, text=True)
            results.append({"step": st, "stdout": norm(p.stdout, home), "stderr": norm(p.stderr, home), "exit": p.returncode})
        elif op == "read":
            f = home / st[1]
            results.append({"step": st, "content": f.read_text() if f.exists() else None})
        elif op == "exists":
            results.append({"step": st, "exists": os.path.lexists(home / st[1])})
    out[name] = {"steps": steps, "results": results}

(Path(__file__).parent / "subagent_symlink_vectors.json").write_text(json.dumps(out, indent=1, sort_keys=True) + "\n")
