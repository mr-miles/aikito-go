"""Generate utf8_replace_vectors.json: CPython bytes.decode('utf-8', 'replace').

Run from the repo root: python3 internal/compat/testdata/gen_utf8_replace_vectors.py
"""
import json, random
from pathlib import Path

crafted = [
    b"plain", b"caf\xc3\xa9", b"\xff", b"\xff\xfe\xfd", b"a\x80b", b"\xc3", b"\xc3(",
    b"\xe2\x82", b"\xe2\x82(", b"\xe2\x82\xac", b"\xe2(\xa1", b"\xf0\x9f\x98", b"\xf0\x9f\x98\x80",
    b"\xf0\x9f(\x80", b"\xe0\x80\x80", b"\xe0\xa0\x80", b"\xed\xa0\x80", b"\xed\x9f\xbf",
    b"\xf4\x90\x80\x80", b"\xf4\x8f\xbf\xbf", b"\xf5\x80", b"\xc0\xaf", b"\xc1\xbf", b"\xc2\x80",
    b"\xef\xbf\xbd", b"\xf8\x88\x80\x80\x80", b"\x80\x80\x80", b"\xe1\x80\xe1\x80\x80",
    b"line1\r\n\xe9t\xe9\n", b"",
]
rng = random.Random(1234)
alphabet = [0x00, 0x41, 0x7F, 0x80, 0x8F, 0x90, 0x9F, 0xA0, 0xBF, 0xC0, 0xC1, 0xC2, 0xDF,
            0xE0, 0xE1, 0xEC, 0xED, 0xEE, 0xEF, 0xF0, 0xF1, 0xF3, 0xF4, 0xF5, 0xFF]
fuzz = [bytes(rng.choice(alphabet) for _ in range(rng.randint(1, 8))) for _ in range(3000)]
seen, cases = set(), []
for b in crafted + fuzz:
    if b in seen:
        continue
    seen.add(b)
    cases.append([b.hex(), b.decode("utf-8", "replace")])
Path(__file__).with_name("utf8_replace_vectors.json").write_text(
    json.dumps(cases, ensure_ascii=False, indent=0) + "\n", encoding="utf-8")
print(len(cases))
