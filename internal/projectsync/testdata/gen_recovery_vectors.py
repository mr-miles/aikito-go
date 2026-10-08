"""Generate recovery_vectors.json: project-sync crash recovery, from the real Python CLI.

For each scenario, a project is synced once, the workspace changed, and a
second `aikito sync project p1` is hard-killed (os._exit) right after its Nth
transaction-journal write, for every N until the sync completes. That leaves
a genuine mid-transaction state on disk (pending or committed journal,
staging/recovery directories, half-applied checkout). Optional tamper steps
then edit that state (a concurrent edit, a journal path outside the allowed
roots). The resulting home is saved as a fixture, and the reference CLI's
next commands (status, sync project x2) are recorded with the home tree after
each.

The Go test (recovery_vectors_test.go, package cli) recreates each fixture
in its own temp home and replays the commands. Fixtures are relocatable:
home paths are stored as {H}, and binding hashes (sha256 of absolute paths,
used in state file names and journals) are listed with their inputs so the
test can recompute them for its home.

Requires python3 and a reference checkout (AIKITO_PYTHON_SRC, else a sibling
../aikito/src next to this repo). Run from anywhere:

    python3 internal/projectsync/testdata/gen_recovery_vectors.py
"""
import base64
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = os.path.dirname(os.path.abspath(__file__))
SRC = os.environ.get("AIKITO_PYTHON_SRC") or os.path.join(HERE, "..", "..", "..", "..", "aikito", "src")

CRASH_DRIVER = """
import os, sys
import aikito.skill_runtime as r
orig = r.write_transaction_journal
n = [0]
def w(*a, **k):
    res = orig(*a, **k)
    n[0] += 1
    if n[0] == int(os.environ["CRASH_AT"]):
        os._exit(17)
    return res
r.write_transaction_journal = w
from aikito.cli import main
sys.argv = ["aikito"] + sys.argv[1:]
main()
"""

TXID = re.compile(r"[0-9a-f]{32}")
TXID_B = re.compile(rb"[0-9a-f]{32}")


def skill(name, version):
    return f"---\nname: {name}\ndescription: {name} {version}\n---\n\n# {name} {version}\n\nBody {version}.\n"


def project_toml(mode, skills):
    body = ", ".join(f'"{s}"' for s in skills)
    return f'name = "p1"\npath = "~/p1"\nsync_mode = "{mode}"\nskills = [{body}]\n'


# Each scenario: (mode, skills) for the first sync, then edits, then the
# interrupted sync. Edits are (relative path, content or None to delete).
SCENARIOS = {
    "copy_update": {
        "first": ("copy", ["sa", "sb"]),
        "edits": [("aikito/skills/sa/SKILL.md", skill("sa", "v2")), ("aikito/skills/sb/SKILL.md", skill("sb", "v2"))],
    },
    "copy_create": {
        "first": ("copy", []),
        "edits": [("aikito/projects/p1/agent.toml", project_toml("copy", ["sa", "sb"]))],
    },
    "copy_deselect": {
        "first": ("copy", ["sa", "sb"]),
        "edits": [("aikito/projects/p1/agent.toml", project_toml("copy", []))],
    },
    "link_create": {
        "first": ("link", []),
        "edits": [("aikito/projects/p1/agent.toml", project_toml("link", ["sa", "sb"]))],
    },
    "copy_to_link": {
        "first": ("copy", ["sa", "sb"]),
        "edits": [("aikito/projects/p1/agent.toml", project_toml("link", ["sa", "sb"]))],
    },
    "link_to_copy": {
        "first": ("link", ["sa", "sb"]),
        "edits": [("aikito/projects/p1/agent.toml", project_toml("copy", ["sa", "sb"]))],
    },
    # Journals with file pre/post images (the files list): a config change
    # during sync, and rm skill's selection transaction.
    "sync_new_checkout_path": {
        "first": ("copy", ["sa"]),
        "edits": [],
        "mkdirs": ["p1b"],
        "crash_cmd": ["sync", "project", "p1", "{H}/p1b"],
    },
    "rm_skill_project": {
        "first": ("copy", ["sa", "sb"]),
        "edits": [],
        "crash_cmd": ["rm", "skill", "sa", "--project", "p1"],
    },
    "rm_skill_force": {
        "first": ("copy", ["sa", "sb"]),
        "edits": [],
        "crash_cmd": ["rm", "skill", "sa", "--force"],
    },
}


def env_for(home):
    return dict(os.environ, HOME=home, PATH="/usr/bin:/bin", PYTHONPATH=os.path.abspath(SRC),
                COLUMNS="80", LANG="C.UTF-8")


def run(home, *args):
    return subprocess.run([sys.executable, "-m", "aikito", *args], cwd=home, env=env_for(home),
                          capture_output=True, text=True)


def setup(home, sc):
    os.makedirs(f"{home}/.claude")
    os.makedirs(f"{home}/p1")
    assert run(home, "init", "workspace").returncode == 0
    for s in ("sa", "sb"):
        Path(f"{home}/aikito/skills/{s}").mkdir(parents=True)
        Path(f"{home}/aikito/skills/{s}/SKILL.md").write_text(skill(s, "v1"))
    os.makedirs(f"{home}/aikito/projects/p1/memory/notes")
    Path(f"{home}/aikito/projects/p1/AGENTS.md").write_text("")
    Path(f"{home}/aikito/projects/p1/agent.toml").write_text(project_toml(*sc["first"]))
    first = run(home, "sync", "project", "p1")
    assert first.returncode == 0, first.stderr
    for rel, content in sc["edits"]:
        p = Path(home, rel)
        if content is None:
            p.unlink()
        else:
            p.write_text(content)
    for d in sc.get("mkdirs", []):
        os.makedirs(f"{home}/{d}")


def crash(home, n, sc):
    cmd = [a.replace("{H}", home) for a in sc.get("crash_cmd", ["sync", "project", "p1"])]
    p = subprocess.run([sys.executable, "-c", CRASH_DRIVER, *cmd], cwd=home,
                       env=dict(env_for(home), CRASH_AT=str(n)), capture_output=True, text=True)
    return p.returncode == 17


def binding_hashes(home):
    """Every binding hash in state file names and journals, with its inputs."""
    found = {}
    state_dir = Path(home, ".local/state/aikito/project-skills")
    docs = []
    for f in state_dir.glob("*.json"):
        docs.append(json.loads(f.read_text()))
    for j in state_dir.glob("transactions/*/journal.json"):
        data = json.loads(j.read_text())
        for t in data.get("state_transitions", []):
            docs.append(t)
            docs.extend(t.get("post_docs", []) or [])
            docs.extend(t.get("pre_docs", []) or [])
    for d in docs:
        ws, proj, co = d.get("workspace_root"), d.get("project_name"), d.get("physical_checkout")
        if ws and proj and co:
            h = hashlib.sha256(f"{ws}:{proj}:{co}".encode()).hexdigest()
            found[h] = {"hash": h, "ws": ws.replace(home, "{H}"), "proj": proj, "co": co.replace(home, "{H}")}
    return sorted(found.values(), key=lambda x: x["hash"])


def normalize_bytes(data, home, hashes):
    data = data.replace(home.encode(), b"{H}")
    for h in hashes:
        data = data.replace(h["hash"].encode(), b"{BINDING}")
    return data


def snapshot(home, hashes, blobs):
    entries = []
    for root, dirs, files in os.walk(home):
        dirs[:] = sorted(d for d in dirs if not (root == f"{home}/aikito" and d == ".git"))
        rel_root = os.path.relpath(root, home)
        for d in dirs:
            p = os.path.join(root, d)
            rel = os.path.normpath(os.path.join(rel_root, d))
            if os.path.islink(p):
                entries.append({"path": rel, "kind": "link", "target": os.readlink(p).replace(home, "{H}")})
            else:
                entries.append({"path": rel, "kind": "dir", "mode": os.stat(p).st_mode & 0o777})
        for f in sorted(files):
            p = os.path.join(root, f)
            rel = os.path.normpath(os.path.join(rel_root, f))
            if os.path.islink(p):
                entries.append({"path": rel, "kind": "link", "target": os.readlink(p).replace(home, "{H}")})
                continue
            data = Path(p).read_bytes().replace(home.encode(), b"{H}")
            digest = hashlib.sha256(data).hexdigest()
            blobs[digest] = base64.b64encode(data).decode()
            entries.append({"path": rel, "kind": "file", "blob": digest, "mode": os.stat(p).st_mode & 0o777})
    # os.walk lists symlinked dirs among dirs without descending: fine.
    return entries


def tree(home, hashes):
    """Comparable listing: every entry except the workspace's .git; files by
    digest of normalised content; transaction ids and binding hashes masked."""
    lines = []
    for root, dirs, files in os.walk(home):
        dirs[:] = [d for d in dirs if not (root == f"{home}/aikito" and d == ".git")]
        for name in dirs + files:
            p = os.path.join(root, name)
            rel = os.path.relpath(p, home)
            for h in hashes:
                rel = rel.replace(h["hash"], "{BINDING}")
            rel = TXID.sub("TXID", rel)
            if os.path.islink(p):
                lines.append(f"L {rel} -> {TXID.sub('TXID', os.readlink(p).replace(home, '{H}'))}")
            elif os.path.isdir(p):
                lines.append(f"D {rel}")
            else:
                data = TXID_B.sub(b"TXID", normalize_bytes(Path(p).read_bytes(), home, hashes))
                mode = f" {os.stat(p).st_mode & 0o777:o}" if rel.startswith(".local") else ""
                lines.append(f"F {rel}{mode} {hashlib.sha256(data).hexdigest()[:16]}")
    return sorted(lines)


def norm_out(text, home, hashes):
    text = text.replace(home, "{H}")
    for h in hashes:
        text = text.replace(h["hash"], "{BINDING}")
    return TXID.sub("TXID", text)


# Tamper steps applied to the crashed home before recovery.
def tamper_edit_target(home):
    """Concurrent modification: edit a file recovery would restore."""
    for p in sorted(Path(home, "p1/.agents/skills").rglob("SKILL.md")):
        p.write_text(p.read_text() + "edited after the crash\n")
        return True
    return False


def tamper_journal(home, key):
    """Point a journal path outside the allowed roots."""
    changed = False
    for j in Path(home, ".local/state/aikito/project-skills/transactions").glob("*/journal.json"):
        data = json.loads(j.read_text())
        outside = f"{home}/elsewhere/x"
        if key == "checkout_paths" and data.get("checkout_paths"):
            data["checkout_paths"] = [f"{home}/elsewhere"]
            changed = True
        if key == "files":
            for entry in data.get("files", []):
                entry["path"] = f"{home}/elsewhere/x/keep.txt"
                changed = True
        for rec in data.get("recovery_dirs", []):
            if key in rec and rec[key]:
                rec[key] = outside
                changed = True
        j.write_text(json.dumps(data, indent=2))
    if changed:
        os.makedirs(f"{home}/elsewhere/x", exist_ok=True)
        Path(f"{home}/elsewhere/x/keep.txt").write_text("must survive recovery\n")
    return changed


TAMPERS = {
    "edited_target": tamper_edit_target,
    "journal_recovery_dir_outside": lambda h: tamper_journal(h, "recovery_dir"),
    "journal_staging_dir_outside": lambda h: tamper_journal(h, "staging_dir"),
    "journal_target_outside": lambda h: tamper_journal(h, "target_path"),
    "journal_checkout_unauthorized": lambda h: tamper_journal(h, "checkout_paths"),
    "journal_file_outside": lambda h: tamper_journal(h, "files"),
}

COMMANDS = [["status"], ["sync", "project", "p1"], ["sync", "project", "p1"]]
# An edited target makes the planner report drift before recovery runs;
# --force gets past the planner so recovery's own concurrent-edit check is
# what decides.
TAMPER_COMMANDS = {"edited_target": [["status"], ["sync", "project", "p1", "--force"], ["sync", "project", "p1"]]}


def record(home, hashes, commands=COMMANDS):
    steps = []
    for args in commands:
        p = run(home, *args)
        steps.append({"args": args, "exit": p.returncode,
                      "stdout": norm_out(p.stdout, home, hashes), "stderr": norm_out(p.stderr, home, hashes),
                      "tree": tree(home, hashes)})
    return steps


def main():
    blobs = {}
    cases = {}
    for name, sc in SCENARIOS.items():
        n = 1
        while True:
            home = os.path.realpath(tempfile.mkdtemp())
            try:
                setup(home, sc)
                if not crash(home, n, sc):
                    break  # the sync completed: no more crash points
                hashes = binding_hashes(home)
                base = {"fixture": snapshot(home, hashes, blobs), "hashes": hashes}
                # Untampered recovery.
                cases[f"{name}/crash{n}"] = dict(base, steps=record(home, hashes))
            finally:
                shutil.rmtree(home, ignore_errors=True)
            # Tampered variants of the same crash point.
            for tname, fn in TAMPERS.items():
                home = os.path.realpath(tempfile.mkdtemp())
                try:
                    setup(home, sc)
                    assert crash(home, n, sc)
                    if not fn(home):
                        continue
                    hashes = binding_hashes(home)
                    cases[f"{name}/crash{n}/{tname}"] = {
                        "fixture": snapshot(home, hashes, blobs), "hashes": hashes,
                        "steps": record(home, hashes, TAMPER_COMMANDS.get(tname, COMMANDS))}
                finally:
                    shutil.rmtree(home, ignore_errors=True)
            n += 1
    out = {"blobs": blobs, "cases": cases}
    with open(os.path.join(HERE, "recovery_vectors.json"), "w") as f:
        json.dump(out, f, indent=0, sort_keys=True)
        f.write("\n")
    print(len(cases), "cases,", len(blobs), "blobs")


if __name__ == "__main__":
    main()
