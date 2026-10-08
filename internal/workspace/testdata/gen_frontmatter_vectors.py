"""Generate frontmatter_vectors.json from frontmatter._parse_markdown_frontmatter.

Each case is {"content", "platforms", "meta", "body"}; meta is json.dumps'd
with sort_keys so map order doesn't matter.
Run from the repo root:
    AIKITO_PYTHON_SRC=../aikito/src python3 internal/workspace/testdata/gen_frontmatter_vectors.py
"""
import json
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(os.environ.get("AIKITO_PYTHON_SRC", "../aikito/src")).resolve()))
from aikito.frontmatter import _parse_markdown_frontmatter  # noqa: E402

CASES = [
    ("", []),
    ("no frontmatter\n", []),
    ("---\nname: a\n---\nbody\n", []),
    ("﻿---\nname: a\n---\nbody\n", []),
    ("  \n---\nname: a\n---\n  body  \n\n", []),
    ("---\r\nname: a\r\ndescription: b c\r\n---\r\nbody\r\n", []),
    ("---\nname: a\n", []),
    (" ---\nname: a\n---\n", []),
    ("---  \nname: a\n---\t\nbody", []),
    ("---\ndescription: >-\n  one\n  two\n\n  three\nx: 1\n---\nb", []),
    ("---\ndescription: |\n  one\n  two\n# c\n---\nb", []),
    ("---\ndesc: >\n  a\n---\nb", []),
    ("---\ntools:\n  - read\n  - 'edit'\n  - \"x\"\n---\nb", []),
    ("---\ntools: [read, edit]\n---\nb", []),
    ("---\ntools: [\"read\", \"edit\"]\n---\nb", []),
    ("---\ntools: []\n---\nb", []),
    ("---\nopts: {a: 1, 'b': x}\n---\nb", []),
    ("---\nopts: {\"a\": 1, \"b\": [true, null]}\n---\nb", []),
    ("---\nopts: {}\n---\nb", []),
    ("---\nflag: TRUE\nother: False\nnone: ~\nnul: null\nnum: 42\nflt: 1.5\n---\nb", []),
    ("---\nquoted: \"hello: world\"\nsingle: 'it''s'\n---\nb", []),
    ("---\nempty:\nnext: v\n---\nb", []),
    ("---\nnested:\n  plain text\n  more text\n---\nb", []),
    ("---\nclaude-code:\n  model: opus\n  effort: high\n---\nb", ["claude-code"]),
    ("---\nclaude-code:\n  model: opus\n---\nb", []),
    ("---\n# comment\n  indented: no\nkey without colon\nk: v\n---\nb", []),
    ("---\nk: v\n---\n---\nsecond block\n", []),
    ("---\nk: a:b:c\n---\nb", []),
    ("---\nk: [1, 2\n---\nb", []),
    ("---\nk: v still\n---\nb", []),
    ("---\nk: v\n---\n　body　\n", []),
]


def main():
    out = []
    for content, platforms in CASES:
        meta, body = _parse_markdown_frontmatter(content, platform_names=platforms)
        out.append({
            "content": content,
            "platforms": platforms,
            "meta": json.dumps(meta, sort_keys=True, ensure_ascii=False),
            "body": body,
        })
    Path(__file__).with_name("frontmatter_vectors.json").write_text(
        json.dumps(out, indent=1, ensure_ascii=False) + "\n", encoding="utf-8"
    )
    print(f"wrote {len(out)} cases")


if __name__ == "__main__":
    main()
