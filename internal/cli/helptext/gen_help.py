"""Capture the reference Python CLI's --help output for every command.

Writes help.json next to this script: {"<command path>": "<help text>", ...}
plus "__noargs__" (the stderr of running `aikito` with no arguments). The
text is verbatim; annotations for options this port doesn't implement are
applied at runtime in internal/cli/help.go, so a regeneration diff shows
only genuine upstream changes.

Run from anywhere:  python3 internal/cli/helptext/gen_help.py
Reference checkout: AIKITO_PYTHON_SRC, else a sibling ../aikito/src.
"""
import argparse, json, os, subprocess, sys, tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
SRC = os.environ.get("AIKITO_PYTHON_SRC") or os.path.join(HERE, "..", "..", "..", "..", "aikito", "src")
SRC = os.path.abspath(SRC)
sys.path.insert(0, SRC)
from aikito.cli_parser import build_parser  # noqa: E402

paths = []
def walk(parser, prefix):
    paths.append(prefix)
    for action in parser._actions:
        if isinstance(action, argparse._SubParsersAction):
            for name, sub in action.choices.items():
                walk(sub, prefix + [name])
walk(build_parser(), [])

home = tempfile.mkdtemp()
env = {"PATH": "/usr/bin:/bin", "HOME": home, "PYTHONPATH": SRC, "COLUMNS": "80", "LANG": "C.UTF-8"}

def run(args):
    return subprocess.run([sys.executable, "-m", "aikito", *args], env=env, capture_output=True, text=True)

out = {}
for path in paths:
    r = run(path + ["--help"])
    if r.returncode != 0:
        sys.exit(f"--help failed for {path!r}: {r.stderr}")
    out[" ".join(path)] = r.stdout
r = run([])
out["__noargs__"] = r.stderr
out["__noargs_exit__"] = str(r.returncode)

with open(os.path.join(HERE, "help.json"), "w", encoding="utf-8") as f:
    json.dump(out, f, ensure_ascii=False, indent=1, sort_keys=True)
    f.write("\n")
print(f"captured {len(paths)} help texts")
