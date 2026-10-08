"""Generate internal/cli/testdata/options_vectors.json from the real Python CLI.

Scenarios for options that used to be unimplemented in the Go port
(add skill --project/--global/--sync, add mcp --from/--sync, add subagent
--from, rm skill --project, maintain memory / edit instructions / diff
project current-directory detection). Same step format as
gen_syncglobal_vectors.py, plus:

  {"op": "run", "args": [...], "cwd": "rel/dir", "env": {...}}
      run from a directory under HOME; env values "H/..." are under HOME
  {"op": "wtree"}   snapshot of the workspace (<home>/aikito, minus .git):
                    every file's content, except bundled skills (listed only)

Requires python3 and a reference checkout (AIKITO_PYTHON_SRC, else a sibling
../aikito/src next to this repo). Run from anywhere:

    python3 internal/cli/testdata/gen_options_vectors.py
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from gen_syncglobal_vectors import HERE, SRC, normalize, tree  # noqa: E402

BUNDLED = ("aikito/skills/aikito", "aikito/skills/durable-memory")


def wtree(home):
    lines = []
    root_ws = os.path.join(home, "aikito")
    for root, dirs, files in os.walk(root_ws):
        dirs[:] = sorted(d for d in dirs if d != ".git" and not os.path.islink(os.path.join(root, d)))
        for name in sorted(dirs + files):
            p = os.path.join(root, name)
            rel = os.path.relpath(p, home)
            if os.path.islink(p):
                lines.append(f"L {rel} -> {normalize(os.readlink(p), home)}")
            elif os.path.isdir(p):
                lines.append(f"D {rel}")
            elif any(rel.startswith(b + os.sep) for b in BUNDLED):
                lines.append(f"F {rel}")
            else:
                with open(p, encoding="utf-8", newline="") as f:
                    content = normalize(f.read(), home)
                lines.append(f"F {rel} {json.dumps(content, ensure_ascii=False)}")
    return sorted(lines)


def run_cli(args, home, cwd=None, extra_env=None):
    env = {"HOME": home, "PATH": "/usr/bin:/bin", "PYTHONPATH": os.path.abspath(SRC),
           "COLUMNS": "80", "LANG": "C.UTF-8"}
    for k, v in (extra_env or {}).items():
        env[k] = home + v[1:] if v.startswith("H/") else v
    return subprocess.run(["python3", "-m", "aikito", *args], cwd=os.path.join(home, cwd or ""),
                          env=env, capture_output=True, text=True, stdin=subprocess.DEVNULL)


def run_scenario(steps):
    home = os.path.realpath(tempfile.mkdtemp())
    out = []
    try:
        for step in steps:
            op = step["op"]
            path = os.path.join(home, step.get("path", ""))
            if op == "mkdir":
                os.makedirs(path, exist_ok=True)
            elif op in ("write", "append"):
                os.makedirs(os.path.dirname(path), exist_ok=True)
                with open(path, "a" if op == "append" else "w") as f:
                    f.write(step["content"])
            elif op == "symlink":
                t = step["target"]
                os.symlink(home + t[1:] if t.startswith("H/") else t, path)
            elif op == "rm":
                if os.path.isdir(path) and not os.path.islink(path):
                    shutil.rmtree(path)
                else:
                    os.remove(path)
            elif op == "replace":
                text = Path(path).read_text()
                assert step["old"] in text, (path, step["old"])
                Path(path).write_text(text.replace(step["old"], step["new"]))
            elif op == "init":
                proc = run_cli(["init", "workspace"], home)
                assert proc.returncode == 0, proc.stderr
            elif op == "run":
                proc = run_cli(step["args"], home, step.get("cwd"), step.get("env"))
                out.append({"args": step["args"], "exit": proc.returncode,
                            "stdout": normalize(proc.stdout, home), "stderr": normalize(proc.stderr, home)})
            elif op == "tree":
                out.append({"tree": tree(home)})
            elif op == "wtree":
                out.append({"wtree": wtree(home)})
            elif op == "read":
                out.append({"read": step["path"], "content": normalize(Path(path).read_text(), home)})
            else:
                raise ValueError(op)
    finally:
        shutil.rmtree(home, ignore_errors=True)
    return out


def w(path, content):
    return {"op": "write", "path": path, "content": content}


def run(*args, cwd=None, env=None):
    step = {"op": "run", "args": list(args)}
    if cwd:
        step["cwd"] = cwd
    if env:
        step["env"] = env
    return step


def init(*dirs):
    return [{"op": "mkdir", "path": d} for d in dirs] + [{"op": "init"}]


def project(name, skills=(), sync_mode="link", extra=""):
    body = ", ".join(f'"{s}"' for s in skills)
    return [
        {"op": "mkdir", "path": name},
        w(f"aikito/projects/{name}/agent.toml",
          f'name = "{name}"\npath = "~/{name}"\nsync_mode = "{sync_mode}"\nskills = [{body}]\n{extra}'),
        w(f"aikito/projects/{name}/AGENTS.md", ""),
        {"op": "mkdir", "path": f"aikito/projects/{name}/memory/notes"},
    ]


SKILL_SRC = "---\nname: imported\ndescription: An imported skill\nextra: keep\n---\n\n# Imported\n\nBody.\n"
TREE, WTREE = {"op": "tree"}, {"op": "wtree"}

SCENARIOS = {}


def scenario(name, steps):
    SCENARIOS[name] = steps


# --- add skill ---
BASE = init(".claude") + project("p1") + project("p2") + [{"op": "mkdir", "path": "p1/sub"}, {"op": "mkdir", "path": "outside"}]
scenario("add_skill_global_flag", BASE + [run("add", "skill", "g1", "--global", cwd="p1"), WTREE])
scenario("add_skill_detected_project", BASE + [run("add", "skill", "s1", cwd="p1/sub"), WTREE,
                                             run("add", "skill", "s1", cwd="p1"), run("add", "skill", "s1", cwd="p2"), WTREE])
scenario("add_skill_outside_is_global", BASE + [run("add", "skill", "g2", cwd="outside"), WTREE])
scenario("add_skill_project_list", BASE + [run("add", "skill", "s2", "--project", "p1, p2,p1", "--description", "Two"), WTREE,
                                         run("add", "skill", "s2", "--project", "p1,p2"),
                                         run("add", "skill", "s2", "--project=p2")])
scenario("add_skill_project_errors", BASE + [run("add", "skill", "x", "--project", "nope"),
                                           run("add", "skill", "x", "--project", "p1", "--global"),
                                           run("add", "skill", "x", "--project"),
                                           run("add", "skill", "x", "--proj", "p1"),
                                           run("add", "skill", "aikito", "--project", "p1"),
                                           run("add", "skill", "durable-memory", "--project", "p1"),
                                           run("add", "skill", "durable-memory", "--project", "p1"), WTREE])
scenario("add_skill_project_toml_shapes", init(".claude")
         + project("p3", extra='\n[extra]\nkey = "v"\n')
         + [w("aikito/projects/p4/agent.toml", 'name = "p4"\n# comment\n\n[table]\nx = 1\n'),
            w("aikito/projects/p5/agent.toml", 'name = "p5"\nskills = "notalist"\n'),
            run("add", "skill", "t1", "--project", "p3"), run("add", "skill", "t1", "--project", "p4"),
            run("add", "skill", "t2", "--project", "p5"), WTREE])
scenario("add_skill_from_project_and_force", BASE
         + [w("src/imported/SKILL.md", SKILL_SRC), w("src/imported/ref/notes.md", "ref\n"),
            {"op": "mkdir", "path": "src/imported/.git"}, w("src/imported/.git/HEAD", "x\n"),
            run("add", "skill", "--from", "src/imported", "--project", "p1", "--description", "Overridden"), WTREE,
            run("add", "skill", "--from", "src/imported", "--project", "p1"),
            w("src/imported/SKILL.md", SKILL_SRC.replace("Body.", "Body v2.")),
            run("add", "skill", "--from", "src/imported", "--project", "p1,p2", "--force"), WTREE])
scenario("add_skill_from_file_frontmatter", init(".claude")
         + [w("src/my-tool.md", "---\nname: >\n  folded-name\ndescription: |\n  Multi\n  line\n---\nBody\n"),
            w("src/plain/SKILL.md", "No frontmatter\n"),
            w("src/quoted.md", '---\nname: "quoted-skill"\ndescription: \'single: quoted\'\n---\nB\n'),
            run("add", "skill", "--from", "src/my-tool.md"), run("add", "skill", "--from", "src/plain"),
            run("add", "skill", "--from", "src/quoted.md"), WTREE])
scenario("add_skill_global_sync", init(".claude") + [run("add", "skill", "gs", "--global", "--sync"), TREE, WTREE])
scenario("add_skill_project_sync", BASE + [run("add", "skill", "ps", "--project", "p1", "--sync"), TREE, WTREE])
scenario("add_skill_project_sync_blocked", BASE
         + [{"op": "mkdir", "path": "p1/.agents/memory/notes"}, w("p1/.agents/memory/notes/x.md", "x\n"),
            run("add", "skill", "pb", "--project", "p1", "--sync"), TREE, WTREE])
scenario("add_skill_sync_global_conflict", init(".claude")
         + [w(".claude/CLAUDE.md", "mine\n"), run("add", "skill", "gc", "--sync"), TREE, WTREE])
scenario("add_skill_two_projects_conflict", init(".claude") + project("q1") + project("q2")
         + [w("aikito/projects/q2/agent.toml", 'name = "q2"\npath = "~/q1"\nskills = []\n'),
            run("add", "skill", "c1", cwd="q1")])


def main():
    vectors = {name: {"steps": steps, "expect": run_scenario(steps)} for name, steps in SCENARIOS.items()}
    with open(os.path.join(HERE, "options_vectors.json"), "w") as f:
        json.dump(vectors, f, indent=1, sort_keys=True, ensure_ascii=False)
        f.write("\n")


if __name__ == "__main__":
    main()
