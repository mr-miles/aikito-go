"""Generate rm_usage_vectors.json: argparse usage errors for rm/remove.

Run from the repo root: python3 internal/cli/testdata/gen_rm_usage_vectors.py
These all fail in argument parsing, before any workspace access.
"""
import json, os, subprocess, sys, tempfile
from pathlib import Path

src = Path(__file__).resolve().parents[3].parent / "aikito" / "src"
home = tempfile.mkdtemp()
env = dict(os.environ, HOME=home, PYTHONPATH=str(src), COLUMNS="80", LANG="C.UTF-8")
out = []
for verb in ("rm", "remove"):
    for rest in ([], ["skill"], ["skills"], ["subagent"], ["subagents"], ["mcp"], ["mcps"],
                 ["memory"], ["inbox"], ["bogus"], ["skill", "a", "b"], ["mcp", "--bogus", "x"],
                 ["memory", "--sync", "x"], ["subagent", "--sy"], ["skill", "--project"],
                 ["inbox", "a", "b"]):
        args = [verb, *rest]
        p = subprocess.run([sys.executable, "-m", "aikito", *args], env=env, capture_output=True, text=True)
        out.append({"args": args, "stdout": p.stdout, "stderr": p.stderr, "exit": p.returncode})
Path(__file__).with_name("rm_usage_vectors.json").write_text(json.dumps(out, indent=1) + "\n")
