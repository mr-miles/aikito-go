"""Generate strictjson_vectors.json: json.loads with layout.py's strict hooks.

Run from the repo root: python3 internal/workspace/testdata/gen_strictjson_vectors.py
Outcome per input: ["ok", <canonical value>] | ["dup"] | ["const", name] | ["error"].
"""
import json, random
from pathlib import Path

class Dup(Exception): pass
class Const(Exception): pass

def pairs(ps):
    d = {}
    for k, v in ps:
        if k in d:
            raise Dup()
        d[k] = v
    return d

def const(v):
    raise Const(v)

def outcome(text):
    try:
        v = json.loads(text, object_pairs_hook=pairs, parse_constant=const)
    except Dup:
        return ["dup"]
    except Const as e:
        return ["const", e.args[0]]
    except json.JSONDecodeError:
        return ["error"]
    return ["ok", json.dumps(v, sort_keys=True, ensure_ascii=False)]

crafted = [
    '{}', '[]', '""', '"a\\u00e9\\n"', '1', '-0', '1.5', '1e3', '1E-3', '-1.25e+10', '01', '1.', '.5',
    '-', '--1', '+1', 'true', 'false', 'null', 'tru', 'nul', 'NaN', 'Infinity', '-Infinity',
    '[NaN]', '{"a": Infinity}', '{"a":1,"a":2}', '{"a":{"b":1,"b":2},"c":NaN}', '{"a":1,"a":NaN}',
    '[1,]', '[,1]', '{"a":1,}', '{,}', '{"a" 1}', '{"a":}', '{1:2}', '{"a":1}}', '[1]]', ' [1] ',
    '\t{"a" : [1 , 2]}\r\n', '"unterminated', '"bad \\x escape"', '"tab\there"', '[1 2]',
    '{"a":[{"b":[{"c":1,"c":2}]}]}', '[-Infinity, NaN]', '[1, -Inf]', '"\\ud83d\\ude00"', '[[[]]]',
    '{"z":1,"a":2}', '1 2', '', '   ', '[true,false,null]', '{"a":"x","b":[1.0,2e2]}', '-NaN',
]
rng = random.Random(7)
toks = ['{', '}', '[', ']', ',', ':', ' ', '"a"', '"b"', '1', '-1', '0.5', 'true', 'null', 'NaN', '-Infinity']
fuzz = ["".join(rng.choice(toks) for _ in range(rng.randint(1, 9))) for _ in range(4000)]
def tree(depth):
    r = rng.random()
    if depth > 3 or r < 0.3:
        return rng.choice(['1', '"s"', 'null', 'NaN', '-Infinity', 'Infinity', '2.5', 'true'])
    if r < 0.6:
        return "[" + ", ".join(tree(depth + 1) for _ in range(rng.randint(0, 3))) + "]"
    keys = [rng.choice('abc') for _ in range(rng.randint(0, 4))]
    return "{" + ", ".join(f'"{k}": {tree(depth + 1)}' for k in keys) + "}"
fuzz += [tree(0) for _ in range(3000)]
seen, cases = set(), []
for t in crafted + fuzz:
    if t not in seen:
        seen.add(t)
        cases.append([t, outcome(t)])
Path(__file__).with_name("strictjson_vectors.json").write_text(json.dumps(cases, ensure_ascii=False, indent=0) + "\n")
print(len(cases), sum(1 for _, o in cases if o[0] == "ok"), sum(1 for _, o in cases if o[0] == "dup"), sum(1 for _, o in cases if o[0] == "const"))
