"""Generate internal/mcp/testdata/redact_vectors.json from the real Python redact.py.

Run from anywhere:  python3 internal/mcp/testdata/gen_redact_vectors.py
"""
import json, os, sys, unicodedata
# Reference checkout: AIKITO_PYTHON_SRC, else a sibling ../aikito/src next to this repo.
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.environ.get("AIKITO_PYTHON_SRC") or os.path.join(HERE, "..", "..", "..", "..", "aikito", "src"))
from aikito.mcp import redact as R

params = sorted(R.SENSITIVE_URL_PARAMETERS) + [p.upper() for p in sorted(R.SENSITIVE_URL_PARAMETERS)] + [
    # segment matches
    "my_token_value", "user.password.hash", "x-passwd", "db_credentials_ref", "request-signature-v2", "id.jwt", "my-apikey-here",
    # prefixes
    "auth_mode", "AUTH_STATE", "oauth_state", "oauth.nonce",
    # suffixes
    "turkey_key", "github-token", "client.secret", "db_password", "user_pass", "hmac_sig", "req_signature",
    "aws_credential", "svc_credentials", "id_jwt", "gh_pat",
    # must NOT be flagged (over-redaction guards)
    "author", "authority", "authentication_mode", "private_mode", "page", "limit", "q", "redirect_uri",
    "client_id", "state", "scope", "keyboard", "keys", "tokenizer", "passage", "signatures", "api", "monkey",
    "secretary", "passport", "patch", "codex", "sessionid", "keyring", "authz", "",
]
headers = [
    "Authorization", "authorization", "Proxy-Authorization", "X-Auth-Token", "X-API-Key", "x-api_key", "X-Apikey",
    "Cookie", "Set-Cookie", "x-secret-sauce", "Password", "X-Password-Hint", "tokenizer-version",
    # negatives
    "Content-Type", "Accept", "X-Request-Id", "User-Agent", "authorisation", "X-Api", "Session", "Mcp-Session-Id", "",
]
loopback = [
    # case-insensitive host (urlsplit().hostname lowercases)
    "http://LOCALHOST:8080/mcp", "http://Foo.LocalHost/x", "http://LOCALHOST.evil.com/x",
    "http://localhost/", "http://localhost:8080/mcp", "https://foo.localhost/x", "http://127.0.0.1:9/",
    "http://127.0.0.2/", "http://127.255.255.254/", "http://[::1]:9/", "http://[::1]/",
    "http://[::ffff:127.0.0.1]/",
    # negatives
    "http://localhost.evil.com/", "http://notlocalhost/", "http://evillocalhost/", "http://example.com/",
    "http://10.0.0.1/", "http://192.168.1.1/", "http://[::2]/", "http://0.0.0.0/", "http://127.1/",
    "mcp.example.com", "", "file:///tmp/x", "http:///nohost",
]
qurls = [
    # parse_qs drops blank-valued and bare keys
    "https://a.example/x?token=", "https://a.example/x?token", "https://a.example/x?token=&page=2", "https://a.example/x?API_KEY=abc", "https://a.example/x?%74oken=abc",
    "https://x/?TOKEN=a", "https://x/?a=1&token=2", "https://x/?api-key=z", "https://x/?page=2&limit=5",
    "https://x/cb?code=abc&state=xyz", "https://x/", "https://x/?author=me", "https://x/?X-Amz-Signature=deadbeef",
]
probe_cases = [
    ("connection refused to https://mcp.example.com", {"Authorization": "Bearer sk-live-123"}),
    ("401 Unauthorized: token sk-live-123 rejected (Bearer sk-live-123)", {"Authorization": "Bearer sk-live-123"}),
    ("bad key abcdef and abcdefXYZ seen", {"X-API-Key": "abcdef", "X-Auth-Token": "abcdefXYZ"}),
    ("cookie session=s3cr3t leaked", {"Cookie": "session=s3cr3t", "Accept": "application/json"}),
    ("value acceptjson stays", {"Accept": "acceptjson"}),
    ("auth header Basic dXNlcjpwYXNz", {"authorization": "Basic dXNlcjpwYXNz"}),
    ("scheme-only Bearer here", {"Authorization": "Bearer"}),
    ("empty header value", {"Authorization": ""}),
    ("tabs\tand\nnewlines\r\nand\x1b[31mANSI\x1b[0m and\x00nul", {}),
    ("zero​width em space ideo　space bidi‮override soft­hyphen nbsp x", {}),
    ("   leading and   trailing   ", {}),
    ("emoji \U0001F600 and CJK 中文 and combining é stay", {}),
    ("x" * 350, {}),
    ("é" * 320, {}),
    ("secret " * 60 + "TAIL", {"X-Secret": "secret"}),
]
partitions = [("Bearer token", " "), ("Bearer", " "), (" leading", " "), ("trailing ", " "), ("", " "), ("a b c", " "), ("abc", "bc"), ("aXbXc", "X")]
texts = [
    "Open https://auth.example.com/authorize?client_id=a&redirect_uri=b in your browser.",
    "see (https://example.com/a), then https://example.com/b; and https://example.com/c]",
    "callback: https://app.local/cb?code=abc&state=xyz done",
    "no urls here",
    "quoted 'https://q.example.com/x' and <https://angle.example.com/y> and \"https://dq.example.com/z\"",
    "ftp://not.matched/x http://plain.example.com/p.",
    "two: https://a.example.com/?token=1 https://b.example.com/?page=1",
]
authz = [
    # case of query keys, blank values, and userinfo in netloc
    "https://idp.example/cb?CLIENT_ID=a&REDIRECT_URI=b", "https://idp.example/cb?client_id=&redirect_uri=b", "https://oauth@idp.example/cb", "https://user@idp.example/authorize",
    "https://auth.example.com/authorize?client_id=a",
    "https://accounts.example.com/o/oauth2/auth?client_id=a",
    "https://example.com/cb?client_id=a&redirect_uri=b",
    "https://example.com/cb?client_id=a&redirect_uri=b&token=c",
    "https://example.com/authorize?code=abc",
    "https://example.com/login?next=/home",
    "https://example.com/cb?client_id=a",
    "https://example.com/cb?redirect_uri=b",
    "https://AUTH.example.com/AUTHORIZE",
]
v = {
    "sensitive_param": [[p, R.is_sensitive_url_parameter(p)] for p in params],
    "credential_header": [[h, R._is_credential_header(h)] for h in headers],
    "loopback": [[u, R._is_loopback_url(u)] for u in loopback],
    "has_sensitive_params": [[u, R._has_sensitive_parameters(u)] for u in qurls],
    "probe_error": [{"text": t, "headers": h, "want": R._redact_probe_error(t, h)} for t, h in probe_cases],
    "partition": [[s, sep] + list(s.partition(sep)) for s, sep in partitions],
    "urls_in_text": [[t, R._urls_in_text(t)] for t in texts],
    "is_authorization_url": [[u, R._is_authorization_url(u)] for u in authz],
    "redact_sensitive_urls": [[t, R._redact_sensitive_urls(t)] for t in texts],
}
# isprintable: run-length ranges [start, end] (inclusive) of printable code points.
ranges, start = [], None
for c in range(0x110001):
    p = c <= 0x10FFFF and not (0xD800 <= c <= 0xDFFF) and chr(c).isprintable()
    if p and start is None: start = c
    elif not p and start is not None: ranges.append([start, c - 1]); start = None
v["printable_ranges"] = ranges
v["python_unicode_version"] = unicodedata.unidata_version
json.dump(v, open(os.path.join(HERE, "redact_vectors.json"), "w"), ensure_ascii=False, indent=1)
print("ranges:", len(ranges), "unicode", unicodedata.unidata_version)
