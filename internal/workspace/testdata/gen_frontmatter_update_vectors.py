"""Generate frontmatter_update_vectors.json from the reference
_update_markdown_frontmatter and _format_yaml_scalar.

Run from the repo root:
  python3 internal/workspace/testdata/gen_frontmatter_update_vectors.py
"""
import json
import os
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, os.environ.get("AIKITO_PYTHON_SRC", str(ROOT.parent / "aikito" / "src")))
from aikito.frontmatter import _format_yaml_scalar, _update_markdown_frontmatter  # noqa: E402

DOCS = [
    "---\nname: old\ndescription: Old desc\n---\n\n# Body\n",
    "---\n# leading comment\ndescription: d\n---\nBody\n",
    "---\nname: x\ndescription: >\n  folded line one\n\n  line two\nother: keep\n---\n\nBody text\n",
    "---\ndescription: |\n  block\n    nested\nname: y\n---\nB",
    "\ufeff---\nname: bom\n---\n\n\n  Body after blanks\n",
    "No frontmatter here\n",
    "\ufeff\n\n  plain body",
    "---\n---\nempty fm\n",
    "---\r\nname: crlf\r\ndescription: c\r\n---\r\nBody\r\n",
    "---\nname:   spaced\nname2: other\n---\n",
    "---\ntitle: t\n---\n",
]
UPDATES = [
    [["name", "new-name"]],
    [["name", "n"], ["description", "A description: with colon"]],
    [["description", "plain"]],
    [["description", "multi\nline"]],
    [["name", "yes"], ["description", " padded "]],
    [["description", ""]],
]
SCALARS = ["", "plain", "a: b", "#x", "-dash", "?q", "@a", "`b", "%p", "true", "No",
           "ON", " lead", "trail ", 'quo"te', "back\\slash", "new\nline", "tab\tx",
           "comma,x", "q?x", "ünïcode", "a{b}", "x|y", "x>y", "a&b", "*star", "!bang"]

out = {
    "updates": [
        {"content": d, "updates": u, "want": _update_markdown_frontmatter(d, dict(u))}
        for d in DOCS for u in UPDATES
    ],
    "scalars": [[s, _format_yaml_scalar(s)] for s in SCALARS],
}
Path(__file__).with_name("frontmatter_update_vectors.json").write_text(
    json.dumps(out, indent=1, ensure_ascii=False) + "\n", encoding="utf-8")
