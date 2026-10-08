"""Generate doctorfix_vectors.json: `aikito doctor --fix` from the reference CLI.

Run from the repo root (reference checkout at ../aikito):
    python3 internal/cli/testdata/gen_doctorfix_vectors.py

Each scenario is a setup (same operations as gen_report_vectors.py) and a
sequence of commands run in order on one HOME. After the commands, every
aikito/agents/*.toml file is captured. Agent files with missing fields are
derived from the bundled templates by dropping lines, so they follow the
templates if those change.
"""
import json
import shutil
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from gen_report_vectors import PY_CLI, PY_SRC, apply_setup, drop_python_only, run  # noqa: E402

TEMPLATES = PY_SRC / "aikito" / "templates" / "agents"


def without(agent, drop):
    """The bundled template for agent with every line starting with one of
    drop removed."""
    text = (TEMPLATES / f"{agent}.toml").read_text(encoding="utf-8")
    kept = [line for line in text.splitlines(keepends=True) if not any(line.startswith(d) for d in drop)]
    return "".join(kept)


def without_table(agent, table):
    """The bundled template with one [table] and its keys removed."""
    lines = (TEMPLATES / f"{agent}.toml").read_text(encoding="utf-8").splitlines(keepends=True)
    out, skipping = [], False
    for line in lines:
        if line.startswith("["):
            skipping = line.strip() == f"[{table}]"
        if not skipping:
            out.append(line)
    return "".join(out)


BASE = [["mkdir", ".claude"], ["mkdir", ".codex"], ["cli", "init", "workspace"]]

SCENARIOS = {
    "nothing_to_fix": {
        "setup": BASE,
        "commands": [["doctor", "--fix"], ["doctor", "--fix", "--json"]],
    },
    "missing_fields": {
        "setup": BASE + [
            # Top-level key, a key before comment lines, and keys whose
            # table header remains.
            ["write", "aikito/agents/codex.toml",
             without("codex", ["skills_path", "command = [", "builtin_mcps", "live_command"])],
            ["write", "aikito/agents/claude-code.toml",
             without_table("claude-code", "agents.claude-code.mcp")],
        ],
        "commands": [["doctor", "--fix"], ["doctor", "--fix"]],
    },
    "missing_fields_json": {
        "setup": BASE + [
            ["write", "aikito/agents/codex.toml", without("codex", ["display_name", "paths ="])],
        ],
        "commands": [["doctor", "--fix", "--json"], ["doctor"]],
    },
    "installed_not_registered": {
        "setup": [["mkdir", ".claude"], ["cli", "init", "workspace"], ["mkdir", ".codex"], ["mkdir", ".pi"]],
        "commands": [["doctor"], ["doctor", "--fix"], ["doctor", "--fix"]],
    },
    "no_trailing_newline": {
        "setup": BASE + [
            ["write", "aikito/agents/codex.toml",
             without("codex", ["auth_command"]).rstrip("\n")],
        ],
        "commands": [["doctor", "--fix"]],
    },
}


def agent_files(home: Path):
    d = home / "aikito" / "agents"
    return {p.name: p.read_text(encoding="utf-8") for p in sorted(d.glob("*.toml"))} if d.is_dir() else {}


def main():
    out = {}
    for name, sc in SCENARIOS.items():
        home = Path(tempfile.mkdtemp(prefix="aikfix-")).resolve()
        try:
            apply_setup(home, sc["setup"], PY_CLI)
            results = [{"args": c, **drop_python_only(run(PY_CLI, home, c))} for c in sc["commands"]]
            out[name] = {"setup": sc["setup"], "commands": results, "agents": agent_files(home)}
        finally:
            shutil.rmtree(home, ignore_errors=True)
    Path(__file__).with_name("doctorfix_vectors.json").write_text(
        json.dumps(out, indent=1, sort_keys=True, ensure_ascii=False) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
