"""Generate initworkspace_vectors.json from the reference `aikito init workspace`.

Run from the repo root: python3 internal/cli/testdata/gen_initworkspace_vectors.py
Each scenario sets up a fresh HOME, then runs commands; outputs are recorded
with HOME written as {H} and backup timestamps as {TS}.
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

# setup ops: ["mkdir", rel] | ["write", rel, text]
SCENARIOS = {
    "fresh_rerun_force": {"setup": [], "cmds": [["init", "workspace"], ["init", "workspace"], ["init", "workspace", "--force"], ["init", "workspace", "--fo"]]},
    "non_empty_unknown_dir": {"setup": [["write", "ws/notes.txt", "x\n"]], "cmds": [["init", "workspace", "{H}/ws"]]},
    "target_is_file": {"setup": [["write", "f", "x\n"]], "cmds": [["init", "workspace", "{H}/f"]]},
    "source_checkout": {"setup": [["write", "src/LICENSE", ""], ["write", "src/README.md", ""], ["write", "src/pyproject.toml", ""]], "cmds": [["init", "workspace", "{H}/src"]]},
    "layout_unsupported": {"setup": [["write", "ws/layout.toml", "version = 1\n"]], "cmds": [["init", "workspace", "{H}/ws"]]},
    "legacy_layout": {"setup": [["write", "ws/agents.toml", ""]], "cmds": [["init", "workspace", "{H}/ws"]]},
    "empty_existing_dir": {"setup": [["mkdir", "ws"]], "cmds": [["init", "workspace", "{H}/ws"]]},
    "claude_detected": {"setup": [["mkdir", ".claude"]], "cmds": [["init", "workspace"], ["init", "workspace"]]},
    "adoptable_instructions": {"setup": [["write", ".claude/CLAUDE.md", "# hi\n"]], "cmds": [["init", "workspace"]]},
    "drifted_bundled_skill": {"setup": [["init"], ["append", "aikito/skills/aikito/SKILL.md", "junk\n"]], "cmds": [["init", "workspace"]]},
    "unknown_args": {"setup": [], "cmds": [["init", "workspace", "a", "b"], ["init", "workspace", "--bogus"]]},
}


def run(home, args):
    env = {"HOME": str(home), "PATH": "/usr/bin:/bin", "PYTHONPATH": str(PYSRC)}
    return subprocess.run([sys.executable, "-m", "aikito", *args], cwd=home, env=env, capture_output=True, text=True)


def norm(text, home):
    text = text.replace(str(home), "{H}")
    return re.sub(r"bundled-skills_\d{8}_\d{6}_\d{6}", "bundled-skills_{TS}", text)


out = {}
for name, sc in SCENARIOS.items():
    home = Path(tempfile.mkdtemp()).resolve()
    for op in sc["setup"]:
        if op[0] == "mkdir":
            (home / op[1]).mkdir(parents=True, exist_ok=True)
        elif op[0] == "write":
            (home / op[1]).parent.mkdir(parents=True, exist_ok=True)
            (home / op[1]).write_text(op[2])
        elif op[0] == "append":
            with open(home / op[1], "a") as f:
                f.write(op[2])
        elif op[0] == "init":
            run(home, ["init", "workspace"])
    steps = []
    for cmd in sc["cmds"]:
        args = [a.replace("{H}", str(home)) for a in cmd]
        p = run(home, args)
        steps.append({"args": cmd, "stdout": norm(p.stdout, home), "stderr": norm(p.stderr, home), "exit": p.returncode})
    tree = sorted(
        norm(str(p.relative_to(home)), home) for p in home.rglob("*")
        if ".git" not in p.relative_to(home).parts
    )
    out[name] = {"setup": sc["setup"], "steps": steps, "tree": tree}

(Path(__file__).parent / "initworkspace_vectors.json").write_text(json.dumps(out, indent=1, sort_keys=True) + "\n")
