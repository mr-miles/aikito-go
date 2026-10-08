"""Generate skill_description_vectors.json from context_footprint.extract_skill_description.

Run from the repo root: python3 internal/cli/testdata/gen_skill_description_vectors.py
"""
import json, sys, tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[3].parent / "aikito" / "src"))
from aikito.context_footprint import extract_skill_description  # noqa: E402

CASES = [
    "---\nname: a\ndescription: plain one line\n---\nbody\n",
    "---\nname: a\ndescription: >\n  folded text\n  over lines\n---\nbody\n",
    "---\nname: a\ndescription: |\n  literal\n  block\n---\n",
    "---\nname: a\ndescription: \"quoted: with colon\"\n---\n",
    "---\nname: a\ndescription: 'single ''quoted'''\n---\n",
    "---\nname: a\ndescription:    padded   \n---\n",
    "---\nname: a\ndescription: \"\"\n---\n",
    "---\nname: a\n---\nno description\n",
    "no frontmatter at all\n",
    "\n\n---\nname: a\ndescription: leading blank lines\n---\n",
    "---\r\nname: a\r\ndescription: crlf line\r\n---\r\nbody\r\n",
    "---\nname: a\ndescription: [list, value]\n---\n",
    "---\nname: a\ndescription: 42\n---\n",
    "---\nname: a\ndescription: café — unicode\n---\n",
    "---\nname: a\ndescription: two\n  continuation line\n---\n",
]
out = []
for text in CASES:
    d = Path(tempfile.mkdtemp())
    (d / "SKILL.md").write_text(text, encoding="utf-8", newline="")
    got = extract_skill_description(d)
    out.append({"skill_md": text, "description": got})
Path(__file__).with_name("skill_description_vectors.json").write_text(
    json.dumps(out, indent=1, ensure_ascii=False) + "\n", encoding="utf-8")
