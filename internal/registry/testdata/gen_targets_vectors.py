"""Generate targets_vectors.json from the reference agents.resolve_targets.

Run from the repo root: python3 internal/registry/testdata/gen_targets_vectors.py
Paths are written relative to the fake home ("~/...") or project ("@/...").
"""
import json, os, sys, tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[3].parent / "aikito" / "src"))
from aikito.agents import BUILTIN_AGENTS, AgentRegistry, bundled_agent_spec, resolve_targets  # noqa: E402

# Each scenario: directories to create under home (and the project).
SCENARIOS = {
    "empty": [],
    "claude_codex": [".claude", ".codex"],
    "all_markers": [".claude", ".codex", ".gemini", ".config/opencode", ".copilot", ".grok", ".pi", ".deepseek"],
    "skills_hub_exists": [".agents/skills", ".claude"],
}

def rel(p, home, proj):
    s = str(p)
    if s.startswith(str(proj)):
        return "@" + s[len(str(proj)):]
    if s.startswith(str(home)):
        return "~" + s[len(str(home)):]
    return s

out = {}
for name, dirs in SCENARIOS.items():
    home = Path(tempfile.mkdtemp()).resolve()
    proj = home / "proj"
    proj.mkdir()
    for d in dirs:
        (home / d).mkdir(parents=True, exist_ok=True)
    aikito = home / "aikito"
    old = os.environ.get("PATH")
    os.environ["PATH"] = "/nonexistent"
    reg = AgentRegistry.from_document({"agents": {n: dict(bundled_agent_spec(n)) for n in BUILTIN_AGENTS}}, home)
    res = {}
    for kind in ("global_skills", "global_instructions", "project_instructions"):
        for active in (False, True):
            ts = resolve_targets(kind, aikito, home, project_path=proj, project_name="p",
                                 active_only=active, registry=reg)
            res[f"{kind}/{'active' if active else 'all'}"] = [
                {"path": rel(t.path, home, proj),
                 "canonical": rel(t.canonical_source, home, proj) if t.canonical_source else "",
                 "consumers": list(t.consumers),
                 "display": list(t.consumer_display_names),
                 "same_object": t.is_same_object}
                for t in ts
            ]
    os.environ["PATH"] = old
    out[name] = {"dirs": dirs, "targets": res}

Path(__file__).with_name("targets_vectors.json").write_text(json.dumps(out, indent=1, sort_keys=True) + "\n")
