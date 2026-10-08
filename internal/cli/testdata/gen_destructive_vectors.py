"""Generate internal/cli/testdata/destructive_vectors.json from the real Python CLI.

Whole-command scenarios for the commands that delete or rename files:
`rename memory`, `rm memory`, `rm inbox`, `rm mcp` (with --sync/--force) and
`doctor --fix`'s local-state cleanup. Same step format and runner as
gen_options_vectors.py; replayed by destructive_test.go.

Requires python3 and a reference checkout (AIKITO_PYTHON_SRC, else a sibling
../aikito/src next to this repo). Run from anywhere:

    python3 internal/cli/testdata/gen_destructive_vectors.py
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from gen_options_vectors import HERE, TREE, WTREE, init, project, run, run_scenario, w  # noqa: E402

NOTE = "---\nname: {0}\ndescription: Note {0}\n---\n\n# {0}\n\n{1}\n"


def note(path, name, body=""):
    return w(path, NOTE.format(name, body))


# Global memory with a web of wikilinks, plus a project with its own notes.
MEMORY = init(".claude") + project("p1") + [
    note("aikito/memory/notes/alpha.md", "alpha", "See [[beta]] and [[beta|the beta note]] and [[beta#Section]]."),
    note("aikito/memory/notes/beta.md", "beta", "Back to [[alpha]]. Not a link: [[betamax]] or beta."),
    note("aikito/memory/notes/betamax.md", "betamax", "Mentions [[beta]] twice: [[beta]]."),
    note("aikito/memory/notes/sub/gamma.md", "gamma", "Nested, links [[alpha]]."),
    note("aikito/projects/p1/memory/notes/beta.md", "beta", "Project beta links [[alpha]] and [[beta]]."),
    note("aikito/projects/p1/memory/notes/delta.md", "delta", "Links [[beta]]."),
]

INBOX = init(".claude") + [
    w("aikito/inbox/idea-one.md", "# one\n"),
    w("aikito/inbox/idea-two.md", "# two\n"),
    w("aikito/inbox/sub/deep.md", "# deep\n"),
    w("aikito/inbox/solo.md", "# solo\n"),
]

MCP = 'transport = "remote"\nurl = "https://example.com/mcp"\nagents = ["claude-code", "codex"]\n'
MCP_WS = init(".claude", ".codex") + [w("aikito/mcps/remote-s.toml", MCP), w("aikito/mcps/other-s.toml", MCP)]
MCP_SYNCED = MCP_WS + [run("sync", "mcp")]

# A stale project-skill state file: written for a checkout that no longer exists.
SKILL = "---\nname: pskill\ndescription: A pskill skill\n---\n\n# pskill\n"
STATE_BASE = init(".claude") + [
    w("aikito/skills/pskill/SKILL.md", SKILL),
] + project("p1", skills=("pskill",), sync_mode="copy") + project("p2", skills=("pskill",), sync_mode="copy")
STATE_SYNCED = STATE_BASE + [run("sync", "project", "p1"), run("sync", "project", "p2")]

SCENARIOS = {
    # rename memory
    "rename_memory_refactors_links": MEMORY + [run("rename", "memory", "global/beta", "beta-two"), WTREE],
    "rename_memory_prefix": MEMORY + [run("rename", "memory", "gam", "gamma-renamed"), WTREE],
    "rename_memory_ambiguous": MEMORY + [run("rename", "memory", "beta", "x"), run("remove", "memory", "beta"), WTREE],
    "rename_memory_project_note": MEMORY + [run("rename", "memory", "p1/delta", "delta-two"), WTREE],
    "rename_memory_errors": MEMORY + [
        run("rename", "memory", "nosuch", "fine"),
        run("rename", "memory", "global/alpha", "Bad Name"),
        run("rename", "memory", "global/alpha", "x" * 51),
        run("rename", "memory", "global/alpha", "betamax"),
        run("rename", "memory", "global/alpha", "alpha"),
        run("rename", "memory", "global/alpha.md", "alpha-two"),
        WTREE],
    "rename_memory_no_workspace": [{"op": "mkdir", "path": ".claude"}, run("rename", "memory", "a", "b")],
    "rename_usage_errors": MEMORY + [run("rename"), run("rename", "bogus"), run("rename", "memory"),
                                     run("rename", "memory", "alpha"), run("rename", "memory", "a", "b", "c"),
                                     run("rename", "memory", "--bogus", "a", "b")],
    # rm memory
    "rm_memory_with_inbound_refs": MEMORY + [run("rm", "memory", "global/beta"), WTREE],
    "rm_memory_no_refs": MEMORY + [run("rm", "memory", "sub/gamma"), run("rm", "memory", "global/sub/gamma"), WTREE],
    "rm_memory_forms": MEMORY + [
        run("remove", "memory", "p1/delta.md"), run("rm", "memory", "nosuch"), run("rm", "memory", "alp"), WTREE],
    # rm inbox
    "rm_inbox_forms": INBOX + [
        run("rm", "inbox", "solo"), run("rm", "inbox", "idea"), run("remove", "inbox", "idea"),
        run("rm", "inbox", "idea-one.md"), run("rm", "inbox", "sub/deep"), run("rm", "inbox", "nosuch"), WTREE],
    "rm_inbox_configured_path": init(".claude") + [
        w("elsewhere/note-a.md", "# a\n"),
        {"op": "replace", "path": "aikito/config.toml", "old": 'path = "inbox"', "new": 'path = "~/elsewhere"'},
        run("rm", "inbox", "note-a"), TREE],
    # rm mcp
    "rm_mcp_workspace_only": MCP_SYNCED + [run("rm", "mcp", "other-s"), WTREE,
                                           {"op": "read", "path": ".claude.json"}],
    "rm_mcp_with_sync": MCP_SYNCED + [run("rm", "mcp", "remote-s", "--sync"), WTREE,
                                      {"op": "read", "path": ".claude.json"},
                                      {"op": "read", "path": ".codex/config.toml"}],
    "rm_mcp_sync_blocked_by_drift": MCP_SYNCED + [
        {"op": "replace", "path": ".claude.json", "old": "https://example.com/mcp", "new": "https://edited.example.com/mcp"},
        run("rm", "mcp", "remote-s", "--sync"), WTREE, {"op": "read", "path": ".claude.json"},
        run("sync", "mcp"), run("sync", "mcp", "--force"), {"op": "read", "path": ".claude.json"}],
    "rm_mcp_force": MCP_SYNCED + [
        {"op": "replace", "path": ".claude.json", "old": "https://example.com/mcp", "new": "https://edited.example.com/mcp"},
        run("rm", "mcp", "remote-s", "--sync", "--force"), WTREE, {"op": "read", "path": ".claude.json"}],
    "rm_mcp_missing": MCP_WS + [run("rm", "mcp", "nosuch"), run("rm", "mcp", "nosuch", "--sync")],
    # doctor --fix local state
    "doctor_fix_stale_state": STATE_SYNCED + [
        {"op": "rm", "path": "p2"}, TREE, run("doctor", "--fix"), TREE, run("doctor", "--fix")],
    "doctor_fix_live_state_kept": STATE_SYNCED + [run("doctor", "--fix"), TREE],
    "doctor_fix_unregistered_project_state": STATE_SYNCED + [
        {"op": "rm", "path": "aikito/projects/p2"}, run("doctor", "--fix"), TREE],
}


def main():
    vectors = {name: {"steps": steps, "expect": run_scenario(steps)} for name, steps in SCENARIOS.items()}
    with open(os.path.join(HERE, "destructive_vectors.json"), "w") as f:
        json.dump(vectors, f, indent=1, sort_keys=True, ensure_ascii=False)
        f.write("\n")


if __name__ == "__main__":
    main()
