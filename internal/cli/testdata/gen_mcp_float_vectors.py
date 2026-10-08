"""Generate mcp_float_vectors.json: OpenCode config written by `sync mcp`
for float overrides, from the reference CLI.

Run from the repo root: python3 internal/cli/testdata/gen_mcp_float_vectors.py
"""
import json, os, subprocess, sys, tempfile
from pathlib import Path

SRC = Path(__file__).resolve().parents[3].parent / "aikito" / "src"
VALUES = ["1.25e10", "-1.25e10", "1.5e-05", "0.0001", "1e16", "9999999999999998.0", "2.5", "123456789012345.6", "3e-5", "100.0"]
out = []
for v in VALUES:
    H = os.path.realpath(tempfile.mkdtemp())
    os.makedirs(f"{H}/.config/opencode")
    env = dict(os.environ, HOME=H, PATH="/usr/bin:/bin", PYTHONPATH=str(SRC))
    run = lambda *a: subprocess.run([sys.executable, "-m", "aikito", *a], cwd=H, env=env, capture_output=True, text=True)
    run("init", "workspace")
    toml = f'agents = ["opencode"]\nurl = "https://example.com/mcp"\ntransport = "remote"\n\n[overrides.opencode]\ntimeout = {v}\n'
    Path(f"{H}/aikito/mcps/num.toml").write_text(toml)
    p = run("sync", "mcp")
    files = sorted(Path(f"{H}/.config/opencode").glob("*.json*"))
    out.append({"toml": toml, "exit": p.returncode, "file": files[0].name, "content": files[0].read_text()})
Path(__file__).with_name("mcp_float_vectors.json").write_text(json.dumps(out, indent=1) + "\n")
