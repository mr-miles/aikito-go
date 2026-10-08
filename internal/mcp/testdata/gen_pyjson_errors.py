"""Generate pyjson_errors.json: CPython json.loads error messages.

Each case is [text, message], message "" when json.loads accepts the text.
Inputs are hand-picked plus a seeded mutation fuzz of valid documents.
Run from the repo root: python3 internal/mcp/testdata/gen_pyjson_errors.py
"""
import json
import random
from pathlib import Path

HAND = [
    "", "  ", "{not json", "{", "[", "{\"a\"", "{\"a\":", "{\"a\":1", "[1", "[1,",
    "{\"a\":1,}", "[1,]", "{\"a\" 1}", "{\"a\":1 \"b\":2}", "[1 2]", "\"abc", "\"a\x01\"",
    "\"a\\q\"", "\"\\u12\"", "\"\\u12zz\"", "\"\\ud800\\u12\"", "\"\\ud800\\uzzzz\"", "\"\\ud800x\"",
    "{\"a\":}", "{} x", "tru", "nul", "-", "01", "1.", "1.e5", "1e", "1e+", "-0", "-x",
    "NaN", "Infinity", "-Infinity", "[NaN, 1]", "\ufeff{}", "{\"é\": [1, 2,, 3]}",
    "{\n  \"a\": 1,\n  \"b\": oops\n}", "\"\\", "{\"a\":1,\"a\":2}", "{ , }", "[ , ]",
    "{\"a\":1,  }", "[1,\n]", "\t\n{}\n\t", "{\"mcpServers\": {\"x\": {\"command\": 'y'}}}",
    "{\"x\": \"\u2028\"}", "\"\\x41\"", "[\"a\" \"b\"]", "{\"a\":[}", "{\"a\":{]}", "[{]",
    "1 2", "true false", "{\"a\":1}}", "[[[]]]]", "\"\u00e9\x05\"", "{\"\x00\":1}",
]

BASES = [
    '{"mcpServers": {"srv": {"command": "npx", "args": ["-y", "pkg"], "env": {"K": "v"}}}}',
    '{"a": [1, 2.5, -3e2, true, false, null], "b": {"c": "d\\n\\u00e9"}}',
    '[{"x": 1}, {"y": [ ]}, "s"]',
]
ALPHABET = list('{}[]:,"\\ \n\tabnu0123456789-+.eE') + ["\x01", "é"]


def message(text):
    try:
        json.loads(text)
        return ""
    except json.JSONDecodeError as exc:
        return str(exc)


def main():
    rng = random.Random(1234)
    cases = [[t, message(t)] for t in HAND]
    seen = set(HAND)
    while len(cases) < len(HAND) + 600:
        text = list(rng.choice(BASES))
        for _ in range(rng.randint(1, 3)):
            op = rng.random()
            pos = rng.randrange(len(text) + 1)
            if op < 0.4 and text:
                del text[min(pos, len(text) - 1)]
            elif op < 0.8:
                text.insert(pos, rng.choice(ALPHABET))
            else:
                text = text[:pos]
        t = "".join(text)
        if t not in seen:
            seen.add(t)
            cases.append([t, message(t)])
    out = Path(__file__).with_name("pyjson_errors.json")
    out.write_text(json.dumps(cases, ensure_ascii=False, indent=0) + "\n", encoding="utf-8")
    print(f"wrote {len(cases)} cases")


if __name__ == "__main__":
    main()
