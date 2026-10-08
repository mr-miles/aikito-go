#!/bin/bash
# Regenerate want.json from the reference CLI. Run from the repo root.
# Each step: [cwd relative to HOME, args...]; HOME is written as {H}.
set -eu
out=internal/cli/testdata/syncproject_cli/want.json
H=$(cd "$(mktemp -d)" && pwd -P)
py() { (cd "$H/$1" && shift && HOME=$H PATH=/usr/bin:/bin PYTHONPATH=$PWD_ROOT/../aikito/src python3 -m aikito "$@"); }
PWD_ROOT=$PWD
mkdir -p "$H/p1" "$H/p2/sub" "$H/elsewhere"
py . init workspace >/dev/null; py . init project p1 "$H/p1" >/dev/null; py . init project p2 "$H/p2" >/dev/null
# Replace the memory notes link with a real directory: a conflict.
rm -f "$H/p1/.agents/memory/notes"; mkdir -p "$H/p1/.agents/memory/notes"; echo x > "$H/p1/.agents/memory/notes/a.md"
python3 - "$H" "$out" <<'PY'
import json, os, subprocess, sys
H, out = sys.argv[1], sys.argv[2]
steps = [
    ["elsewhere", "sync", "project"],
    ["elsewhere", "sync", "project", "."],
    ["p2/sub", "sync", "project"],
    [".", "sync", "project", "p1", "--dry-run"],
    [".", "sync", "project", "p1"],
]
env = dict(os.environ, HOME=H, PATH="/usr/bin:/bin", PYTHONPATH=os.path.abspath("../aikito/src"))
res = []
for cwd, *args in steps:
    p = subprocess.run([sys.executable, "-m", "aikito", *args], cwd=os.path.join(H, cwd), env=env, capture_output=True, text=True)
    norm = lambda s: s.replace(H, "{H}")
    res.append({"cwd": cwd, "args": args, "stdout": norm(p.stdout), "stderr": norm(p.stderr), "exit": p.returncode})
open(out, "w").write(json.dumps(res, indent=1) + "\n")
PY
