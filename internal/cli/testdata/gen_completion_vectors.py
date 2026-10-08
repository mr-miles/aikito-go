"""Generate internal/cli/testdata/completion_vectors.json from the real Python CLI.

Builds a fixture by running the reference `aikito init workspace` into a temp
HOME, layering the extra files below on top, then records what each Python
completion list function returns. completion_test.go builds the identical
fixture with the Go `init workspace` plus the same extra files and compares.

Requires python3 and a reference checkout (AIKITO_PYTHON_SRC, else a sibling
../aikito/src next to this repo). Run from anywhere:

    python3 internal/cli/testdata/gen_completion_vectors.py
"""
import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = os.path.dirname(os.path.abspath(__file__))
SRC = os.environ.get("AIKITO_PYTHON_SRC") or os.path.join(HERE, "..", "..", "..", "..", "aikito", "src")
sys.path.insert(0, SRC)

# Paths are relative to the workspace root (<home>/aikito).
EXTRA_FILES = {
    "skills/my-skill/SKILL.md": "---\nname: my-skill\ndescription: Mine\n---\nBody\n",
    "skills/other-skill/SKILL.md": "---\nname: other-skill\ndescription: Other\n---\nBody\n",
    "subagents/reviewer.md": '---\ndescription: "Reviews"\nagents: ["claude-code"]\n---\nReview.\n',
    "subagents/planner.md": '---\ndescription: "Plans"\nagents: ["claude-code"]\n---\nPlan.\n',
    "subagents/notes.txt": "not a subagent\n",
    "mcps/weather.toml": 'transport = "remote"\nurl = "https://weather.example.com/mcp"\nagents = ["claude-code"]\n',
    "mcps/files.toml": 'command = "npx"\nargs = []\nagents = ["claude-code"]\n',
    "memory/notes/decision-1.md": "# Decision\n",
    "memory/notes/arch.md": "# Arch\n",
    "memory/top-level.md": "# Not under notes/\n",
    "projects/proj1/agent.toml": 'skills = ["my-skill"]\n',
    "projects/proj1/AGENTS.md": "# proj1\n",
    "projects/proj1/memory/notes/decision-1.md": "# Project decision\n",
    "projects/proj1/memory/notes/proj-only.md": "# Project only\n",
    "inbox/idea.md": "idea\n",
    "inbox/sub/nested.md": "nested\n",
    "inbox/readme.txt": "not a note\n",
}

from aikito import completion as C  # noqa: E402

with tempfile.TemporaryDirectory() as home:
    os.makedirs(os.path.join(home, ".claude"))
    env = {"HOME": home, "PATH": "/usr/bin:/bin", "PYTHONPATH": SRC}
    subprocess.run([sys.executable, "-m", "aikito", "init", "workspace"], env=env, check=True, capture_output=True)
    root = Path(home) / "aikito"
    for rel, content in EXTRA_FILES.items():
        p = root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(content, encoding="utf-8")

    os.environ["HOME"] = home
    expected = {
        "projects": C.list_projects(root),
        "skills": C.list_skills(root),
        "subagents": C.list_subagents(root),
        "mcps": C.list_mcps(root),
        "memories": C.list_memories(root),
        "memory-completions": C.list_memory_completions(root),
        "inbox-completions": C.list_inbox_completions(root),
    }

with open(os.path.join(HERE, "completion_vectors.json"), "w") as f:
    json.dump({"extra_files": EXTRA_FILES, "expected": expected}, f, indent=1, sort_keys=True)
for k, v in expected.items():
    print(k, v)
