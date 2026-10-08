"""Generate internal/cli/testdata/unified_diff_vectors.json from Python's difflib.

Each case mirrors diff.py's _unified_diff: "".join(difflib.unified_diff(a, b,
fromfile=..., tofile=...)).rstrip(). Run from anywhere:

    python3 internal/cli/testdata/gen_unified_diff_vectors.py
"""
import difflib
import json
import os

HERE = os.path.dirname(os.path.abspath(__file__))


def lines(*xs):
    return [x + "\n" for x in xs]


ctx = [f"ctx{i}" for i in range(20)]

cases = {
    "both_empty": ([], []),
    "identical": (lines("a", "b", "c"), lines("a", "b", "c")),
    "empty_to_content": ([], lines("a", "b")),
    "content_to_empty": (lines("a", "b"), []),
    "single_line_change": (lines("a"), lines("b")),
    "pure_insert_middle": (lines("a", "b", "c"), lines("a", "b", "X", "c")),
    "pure_delete_middle": (lines("a", "b", "X", "c"), lines("a", "b", "c")),
    "change_at_start": (lines("X", *ctx[:8]), lines("Y", *ctx[:8])),
    "change_at_end": (lines(*ctx[:8], "X"), lines(*ctx[:8], "Y")),
    "insert_at_start": (lines(*ctx[:5]), lines("NEW", *ctx[:5])),
    "append_at_end": (lines(*ctx[:5]), lines(*ctx[:5], "NEW")),
    "two_hunks_far_apart": (
        lines("X", *ctx[:12], "Y"),
        lines("X2", *ctx[:12], "Y2"),
    ),
    "two_changes_exactly_6_apart": (
        lines("X", *ctx[:6], "Y"),
        lines("X2", *ctx[:6], "Y2"),
    ),
    "two_changes_7_apart": (
        lines("X", *ctx[:7], "Y"),
        lines("X2", *ctx[:7], "Y2"),
    ),
    "replace_block": (lines("a", "b", "c", "d"), lines("a", "B1", "B2", "B3", "d")),
    "no_trailing_newline": (["a\n", "b"], ["a\n", "c"]),
    "json_like": (
        lines("{", '  "type": "http",', '  "url": "https://hacked.example.com/mcp"') + ["}"],
        lines("{", '  "type": "http",', '  "url": "https://weather.example.com/mcp"') + ["}"],
    ),
    "redacted_only": (["<redacted value differs>\n"], ["<expected redacted value>\n"]),
    "markdown_subagent": (
        lines("---", "name: reviewer", "description: Old", "---", "", "Body line 1", "Body line 2"),
        lines("---", "name: reviewer", "description: New", "---", "", "Body line 1", "Body line 2", "Body line 3"),
    ),
}

out = []
for name, (a, b) in cases.items():
    got = "".join(difflib.unified_diff(a, b, fromfile="actual: A", tofile="expected: B")).rstrip()
    out.append({"name": name, "a": a, "b": b, "want": got})

with open(os.path.join(HERE, "unified_diff_vectors.json"), "w") as f:
    json.dump(out, f, ensure_ascii=False, indent=1)
print(f"wrote {len(out)} cases")
