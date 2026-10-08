"""Generate mcplive_vectors.json: `aikito show mcp --live` from the reference CLI.

Run from the repo root (reference checkout at ../aikito):
    python3 internal/cli/testdata/gen_mcplive_vectors.py

The probes talk to a local MCP-over-HTTP fixture server (never the network).
mcplive_test.go serves the same responses from httptest. "{URL}" in the setup
and in captured output stands for the fixture's base URL, e.g.
http://127.0.0.1:PORT.

Fixture endpoints (POST only):
  /mcp        initialize, notifications, and two pages of tools/list
  /broken     HTTP 400 with a JSON error message
  /noversion  initialize result without a protocolVersion
"""
import json
import shutil
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from gen_report_vectors import PY_CLI, apply_setup, run  # noqa: E402

PAGES = {"": (["alpha", "beta"], "page-2"), "page-2": (["gamma"], "")}


class Fixture(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def reply(self, code, body=None):
        data = json.dumps(body).encode() if body is not None else b""
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self):
        msg = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
        method, rid = msg.get("method"), msg.get("id")
        if self.path == "/broken":
            return self.reply(400, {"error": {"message": "bad fixture request"}})
        if "id" not in msg:
            return self.reply(202)
        if method == "initialize":
            result = {"capabilities": {}, "serverInfo": {"name": "fixture", "version": "1"}}
            if self.path != "/noversion":
                result["protocolVersion"] = "2025-06-18"
            return self.reply(200, {"jsonrpc": "2.0", "id": rid, "result": result})
        if method == "tools/list":
            names, nxt = PAGES[(msg.get("params") or {}).get("cursor", "")]
            result = {"tools": [{"name": n} for n in names]}
            if nxt:
                result["nextCursor"] = nxt
            return self.reply(200, {"jsonrpc": "2.0", "id": rid, "result": result})
        return self.reply(200, {"jsonrpc": "2.0", "id": rid, "error": {"code": -32601, "message": "no such method"}})


BASE = [["mkdir", ".claude"], ["mkdir", ".codex"], ["cli", "init", "workspace"], ["cli", "sync", "global"]]


def remote(name, path, agents):
    return ["cli", "add", "mcp", name, "--transport", "remote", "--url", "{URL}" + path, "--agents", agents]


SCENARIOS = {
    "live": {
        "setup": BASE + [
            remote("toolbox", "/mcp", "claude-code,codex"),
            remote("broken", "/broken", "claude-code,codex"),
            remote("noversion", "/noversion", "codex"),
            ["cli", "sync", "mcp"],
        ],
        "commands": [
            ["show", "mcp", "--live"],
            ["show", "mcps", "--live", "--color", "always"],
            ["show", "mcp", "toolbox", "--live"],
            ["show", "mcp", "tool", "--live", "--agent", "codex"],
            ["show", "mcp", "toolbox", "--live", "--agent"],
            ["show", "mcp", "broken", "--live"],
            ["show", "mcp", "noversion", "--live", "--agent", "cod"],
            ["show", "mcp", "noversion", "--live", "--agent", "claude-code"],
            ["show", "mcp", "toolbox", "--live", "--no-color"],
        ],
    },
    "live_drifted": {
        # Only servers already in sync are probed by the matrix view.
        "setup": BASE + [
            remote("toolbox", "/mcp", "claude-code,codex"),
            ["cli", "sync", "mcp"],
            ["edit_json", ".claude.json", "mcpServers.toolbox.url", "{URL}/elsewhere"],
        ],
        "commands": [["show", "mcp", "--live"], ["show", "mcp", "toolbox", "--live"]],
    },
}


def main():
    server = ThreadingHTTPServer(("127.0.0.1", 0), Fixture)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    url = f"http://127.0.0.1:{server.server_address[1]}"
    out = {}
    try:
        for name, sc in SCENARIOS.items():
            home = Path(tempfile.mkdtemp(prefix="aiklive-")).resolve()
            try:
                setup = [[a.replace("{URL}", url) for a in step] for step in sc["setup"]]
                apply_setup(home, setup, PY_CLI)
                results = []
                for c in sc["commands"]:
                    r = run(PY_CLI, home, c)
                    results.append({"args": c, **{k: (v.replace(url, "{URL}") if isinstance(v, str) else v) for k, v in r.items()}})
                out[name] = {"setup": sc["setup"], "commands": results}
            finally:
                shutil.rmtree(home, ignore_errors=True)
    finally:
        server.shutdown()
    Path(__file__).with_name("mcplive_vectors.json").write_text(
        json.dumps(out, indent=1, sort_keys=True, ensure_ascii=False) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
