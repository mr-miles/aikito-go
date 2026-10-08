"""Generate float_repr_vectors.json: CPython repr(float) for finite floats.

Run from the repo root: python3 internal/compat/testdata/gen_float_repr_vectors.py
Values are stored as their IEEE-754 bit patterns (hex) so nothing is lost.
"""
import json, random, struct
from pathlib import Path

vals = [0.0, -0.0, 1.0, -1.5, 0.1, 1/3, 1e15, 1e16, 9999999999999998.0, 1.25e10, -1.25e10,
        123456789012345.6, 1e-4, 1e-5, 0.0001234, 0.00001234, 5e-324, 1.7976931348623157e308,
        2.5, 100.0, 1e22, 1e21, 1e100, 12345678901234567890.0, 0.5, 3.14159, 2**53, 2.0**63]
rng = random.Random(99)
for _ in range(4000):
    vals.append(rng.uniform(-1, 1) * 10 ** rng.randint(-30, 30))
for _ in range(2000):
    vals.append(struct.unpack("<d", struct.pack("<Q", rng.getrandbits(64)))[0])
cases = []
for v in map(float, vals):
    if v != v or v in (float("inf"), float("-inf")):
        continue
    cases.append([struct.pack(">d", v).hex(), repr(v)])
Path(__file__).with_name("float_repr_vectors.json").write_text(json.dumps(cases, indent=0) + "\n")
print(len(cases))
