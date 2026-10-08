"""Generate subagent_stale_vectors.json from the reference execute_subagent_plan.

Run from the repo root (AIKITO_PYTHON_SRC defaults to ../aikito/src):
    python3 internal/sync/testdata/gen_subagent_stale_vectors.py

Each case builds a plan, changes the target file the way `mutate` says, then
executes: Python refuses with error_message and leaves the file alone.
Paths are recorded relative to HOME as {H}.
"""
import json
import os
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(Path(os.environ.get("AIKITO_PYTHON_SRC", ROOT.parent / "aikito" / "src")).resolve()))
from aikito.agents import bundled_agent_spec  # noqa: E402,F401
from aikito.subagent import build_subagent_plan, execute_subagent_plan  # noqa: E402
from aikito.templating import load_template  # noqa: E402

SUB = ('---\ndescription: "Reviews code changes for correctness"\nagents: ["claude-code", "dsh"]\n---\n'
       "Review the diff carefully.\n")

# phase: "create" (fresh), "update" (synced, then canonical changed),
# "prune" (synced, then canonical removed, plan with prune=True).
CASES = {
    "create_target_appears": ("create", "write", ".claude/agents/reviewer.md"),
    "update_target_edited": ("update", "write", ".claude/agents/reviewer.md"),
    "update_target_removed": ("update", "remove", ".claude/agents/reviewer.md"),
    "update_target_symlinked": ("update", "symlink", ".claude/agents/reviewer.md"),
    "prune_target_removed": ("prune", "remove", ".claude/agents/reviewer.md"),
    "shared_patch_edited": ("update", "write", ".dsh/cordis.patch.yml"),
}


def setup():
    home = Path(tempfile.mkdtemp()).resolve()
    ws = home / "aikito"
    (ws / "layout.toml").parent.mkdir(parents=True)
    (ws / "layout.toml").write_text("version = 2\n")
    for d in ("agents", "subagents", "mcps", "skills", "memory/notes", "global", "projects"):
        (ws / d).mkdir(parents=True, exist_ok=True)
    for a in ("claude-code", "dsh"):
        (ws / "agents" / f"{a}.toml").write_text(load_template(f"agents/{a}.toml"))
    (home / ".claude").mkdir()
    (home / ".dsh").mkdir()
    (ws / "subagents" / "reviewer.md").write_text(SUB)
    return home, ws


out = {}
os.environ["PATH"] = "/nonexistent"
for name, (phase, mutate, rel) in CASES.items():
    home, ws = setup()
    if phase in ("update", "prune"):
        assert execute_subagent_plan(build_subagent_plan(ws, home), home).success
        if phase == "update":
            (ws / "subagents" / "reviewer.md").write_text(SUB.replace("carefully", "very carefully"))
        else:
            (ws / "subagents" / "reviewer.md").unlink()
    plan = build_subagent_plan(ws, home, allow_empty=True, prune=(phase == "prune"))
    target = home / rel
    if mutate == "write":
        target.parent.mkdir(parents=True, exist_ok=True)
        with open(target, "a") as f:
            f.write("edited by hand\n")
    elif mutate == "remove":
        target.unlink()
    elif mutate == "symlink":
        other = home / "elsewhere.md"
        other.write_text(target.read_text())
        target.unlink()
        target.symlink_to(other)
    before = target.read_bytes() if target.exists() else None
    res = execute_subagent_plan(plan, home)
    after = target.read_bytes() if target.exists() else None
    out[name] = {
        "phase": phase,
        "mutate": mutate,
        "target": rel,
        "success": res.success,
        "error": (res.error_message or "").replace(str(home), "{H}"),
        "unchanged": before == after,
    }

Path(__file__).with_name("subagent_stale_vectors.json").write_text(json.dumps(out, indent=1, sort_keys=True) + "\n")
