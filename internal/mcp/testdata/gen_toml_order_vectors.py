"""Generate toml_order_vectors.json: tomllib's key order for TOML documents.

Each case maps a document to its keys, recursively, in tomllib's dict
order (nested tables as {"key": ..., "keys": [...]} lists).

Run from the repo root: python3 internal/mcp/testdata/gen_toml_order_vectors.py
"""
import json
import tomllib
from pathlib import Path

DOCS = [
    'z = 1\na = 2\nm = 3\n',
    'headers = { Authorization = "a", Cookie = "c", X-Api-Key = "k", Token = "t" }\n',
    '[mcp_servers.zeta]\nurl = "u"\n[mcp_servers.alpha]\nurl = "v"\n[mcp_servers.zeta.env_http_headers]\nB = "1"\nA = "2"\n',
    'b.y = 1\nb.x = 2\na = 3\n[c]\nq.r = 1\np = 2\n',
    '[[arr]]\nz = 1\na = 2\n[[arr]]\nk = 1\nb = 2\n[arr.sub]\ny = 1\nx = 2\n',
    'list = [{ z = 1, a = 2 }, { m = 1, b = 2 }]\nnested = { outer = { z = 1, a = 2 }, b = 1 }\n',
    '[servers."quoted key"]\n"b c" = 1\naa = 2\n[servers.\'lit\']\nz = 1\n',
    'x = 1\n[t2]\nb = 1\n[t1]\na = 1\n[t2.inner]\nz = 1\n',
]


def keys(v):
    if isinstance(v, dict):
        return [[k, keys(x)] for k, x in v.items()]
    if isinstance(v, list):
        return [keys(x) for x in v]
    return None


out = [{"doc": d, "keys": keys(tomllib.loads(d))} for d in DOCS]
Path(__file__).with_name("toml_order_vectors.json").write_text(json.dumps(out, indent=1) + "\n")
