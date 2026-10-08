"""Generate want.json: `aikito diff` on a copied skill edited to hold invalid UTF-8.

Run from the repo root: python3 internal/cli/testdata/diff_invalid_utf8/gen.py
Python decodes with errors="replace": one U+FFFD per maximal invalid subpart.
"""
import json, os, re, subprocess, sys, tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
SRC = HERE.parents[3].parent / "aikito" / "src"
EDITED = bytes.fromhex(
    "2d2d2d0a6e616d653a2073310a6465736372697074696f6e3a20730a2d2d2d0a"  # frontmatter
) + b"stray \xff\xfe bytes, truncated \xe2\x82( euro, overlong \xc0\xaf end\n"

H = os.path.realpath(tempfile.mkdtemp())
env = dict(os.environ, HOME=H, PATH="/usr/bin:/bin", PYTHONPATH=str(SRC), COLUMNS="80", LANG="C.UTF-8")
def run(*args):
    return subprocess.run([sys.executable, "-m", "aikito", *args], cwd=H, env=env, capture_output=True, text=True)
os.makedirs(f"{H}/p1")
run("init", "workspace"); run("init", "project", "p1", f"{H}/p1")
run("add", "skill", "s1", "--description", "s", "--project", "p1")
cfg = Path(f"{H}/aikito/projects/p1/agent.toml")
cfg.write_text(re.sub(r'sync_mode = "link"', 'sync_mode = "copy"', cfg.read_text()))
assert run("sync", "project", "p1").returncode == 0
Path(f"{H}/p1/.agents/skills/s1/SKILL.md").write_bytes(EDITED)
steps = []
for args in (["diff", "project", "p1", "s1"], ["diff", "--all"]):
    p = run(*args)
    steps.append({"args": args, "stdout": p.stdout.replace(H, "{H}"), "stderr": p.stderr.replace(H, "{H}"), "exit": p.returncode})
(HERE / "edited.bin").write_bytes(EDITED)
(HERE / "want.json").write_text(json.dumps(steps, indent=1, ensure_ascii=False) + "\n", encoding="utf-8")
