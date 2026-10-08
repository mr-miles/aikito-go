"""Generate subagent_load_error_vectors.json from the reference CLI.

Run from the repo root: python3 internal/cli/testdata/gen_subagent_load_errors.py
Each case: fresh workspace with a valid subagent 'good', then one extra entry
under subagents/ (or none for the ignored-file case), then the commands.
"""
import json, os, subprocess, sys, tempfile
from pathlib import Path

SRC = Path(__file__).resolve().parents[3].parent / "aikito" / "src"
FM = '---\ndescription: "d"\nagents: ["claude-code"]\n'
CASES = {
    "not_markdown": ("notes.txt", "x\n"),
    "bad_name": ("Bad_Name.md", FM + "---\nBody\n"),
    "directory_entry": ("sub/", None),
    "ignored_clutter": (".DS_Store", "x"),
    "frontmatter_missing": ("fm-missing.md", "Body only\n"),
    "frontmatter_incomplete": ("fm-open.md", FM + "Body\n"),
    "metadata_no_colon": ("no-colon.md", FM + "oops\n---\nBody\n"),
    "invalid_key": ("bad-key.md", FM + 'Bad_Key: {}\n---\nBody\n'),
    "duplicate_key": ("dup-key.md", FM + 'description: "again"\n---\nBody\n'),
    "invalid_value": ("bad-value.md", '---\ndescription: d\nagents: ["claude-code"]\n---\nBody\n'),
    "duplicate_object_key": ("dup-obj.md", FM + 'claude-code: {"model": "a", "model": "b"}\n---\nBody\n'),
    "nan_constant": ("nan.md", FM + 'claude-code: {"x": NaN}\n---\nBody\n'),
    "neg_infinity": ("ninf.md", FM + 'claude-code: {"x": [1, -Infinity]}\n---\nBody\n'),
    "dup_then_nan": ("dupnan.md", FM + 'claude-code: {"a": {"b": 1, "b": 2}, "c": NaN}\n---\nBody\n'),
    "nan_then_dup": ("nandup.md", FM + 'claude-code: {"a": 1, "a": NaN}\n---\nBody\n'),
    "trailing_data": ("trailing.md", FM + 'claude-code: {"a": 1}}\n---\nBody\n'),
    "description_missing": ("no-desc.md", '---\nagents: ["claude-code"]\n---\nBody\n'),
    "agents_invalid": ("bad-agents.md", '---\ndescription: "d"\nagents: []\n---\nBody\n'),
    "platform_config_invalid": ("bad-platform.md", FM + 'claude-code: "x"\n---\nBody\n'),
    "instructions_missing": ("no-body.md", FM + "---\n\n"),
}
COMMANDS = [["sync", "subagents"], ["sync", "--dry-run"]]
out = {}
for name, (entry, content) in CASES.items():
    H = os.path.realpath(tempfile.mkdtemp())
    os.makedirs(f"{H}/.claude")
    env = dict(os.environ, HOME=H, PATH="/usr/bin:/bin", PYTHONPATH=str(SRC), COLUMNS="80", LANG="C.UTF-8")
    run = lambda *a: subprocess.run([sys.executable, "-m", "aikito", *a], cwd=H, env=env, capture_output=True, text=True)
    run("init", "workspace")
    assert run("add", "subagent", "good", "--description", "ok").returncode == 0
    target = Path(f"{H}/aikito/subagents/{entry}")
    if content is None:
        target.mkdir()
    else:
        target.write_text(content, encoding="utf-8")
    steps = []
    for args in COMMANDS:
        p = run(*args)
        steps.append({"args": args, "stdout": p.stdout.replace(H, "{H}"),
                      "stderr": p.stderr.replace(H, "{H}"), "exit": p.returncode})
    out[name] = {"entry": entry, "content": content, "steps": steps}
Path(__file__).with_name("subagent_load_error_vectors.json").write_text(json.dumps(out, indent=1) + "\n")
