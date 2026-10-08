"""Generate vectors.json from the reference implementation.

Run from the repo root:  python3 internal/projectsync/testdata/gen_vectors.py

Covers the pure planners (skill_plan.plan_single_skill, link.plan_link_target)
over their whole input matrix, plus directory fingerprints, binding hashes
and the state-document JSON encoding.
"""
import hashlib
import itertools
import json
import os
import sys
import tempfile
from pathlib import Path

sys.path.insert(
    0,
    os.environ.get("AIKITO_PYTHON_SRC")
    or str(Path(__file__).resolve().parents[3].parent / "aikito" / "src"),
)

from aikito.link import ObservedLink, plan_link_target  # noqa: E402
from aikito.skill_plan import (  # noqa: E402
    DesiredSkill,
    ObservedSkill,
    SkillTarget,
    build_skill_plan,
    plan_single_skill,
)
from aikito.skill_state import (  # noqa: E402
    ProjectSkillStateDocument,
    SkillStateRecord,
    calculate_directory_fingerprint,
    get_binding_hash,
)

T = SkillTarget(
    workspace_root=Path("/ws"),
    workspace_id="ws",
    project_name="proj",
    physical_checkout=Path("/co"),
    skill_name="alpha",
    target_path=Path("/co/.agents/skills/alpha"),
)


def op_dict(op):
    return {
        "action": op.action,
        "rule_id": op.rule_id,
        "reason": op.reason,
        "finding": op.finding or "",
        "requires_force": op.requires_force,
        "force_type": op.force_type or "",
        "is_authorized": op.is_authorized,
        "expected_representation": op.expected_representation,
        "desired_representation": op.desired_representation,
        "expected_fingerprint": op.expected_fingerprint or "",
        "desired_fingerprint": op.desired_fingerprint or "",
        "expected_revision": op.expected_revision,
        "next_state_lifecycle": op.next_state_lifecycle or "",
        "next_baseline_origin": op.next_baseline_origin or "",
    }


skill_cases = []
FPS = {"A": "v1:aaa", "B": "v1:bbb", "C": "v1:ccc", "": None}
for (mode, entry, points, cvalid, cerr, lifecycle, r, b, c, serr, force, offline) in itertools.product(
    ["link", "copy", "absent"],
    ["missing", "dir", "symlink", "unsupported"],
    [False, True],
    [True, False],
    ["", "boom"],
    ["", "active", "inactive"],
    ["A", "B", ""],
    ["A", "B"],
    ["A", "C"],
    ["", "corrupt"],
    [False, True],
    [False, True],
):
    # Prune combinations that cannot change the outcome to keep the file small.
    if offline and (points or not cvalid or cerr or lifecycle or serr or force):
        continue
    if cvalid and cerr:
        continue
    if entry not in ("symlink",) and points:
        continue
    record = (
        SkillStateRecord("alpha", "copy", lifecycle, FPS[b], "write", True) if lifecycle else None
    )
    observed = ObservedSkill(
        target=T,
        entry_type=entry,
        raw_link_target=Path("/elsewhere/alpha") if entry == "symlink" else None,
        resolved_link_target=Path("/elsewhere/alpha") if entry == "symlink" else None,
        link_points_to_canonical=points,
        canonical_valid=cvalid,
        canonical_error=cerr or None,
        runtime_fingerprint=FPS[r],
        canonical_fingerprint=FPS[c],
        state_record=record,
        state_error=serr or None,
        state_revision=3,
    )
    desired = DesiredSkill("alpha", mode, Path("/ws/skills/alpha"), FPS[c])
    op = plan_single_skill(T, desired, observed, force=force, is_offline=offline)
    skill_cases.append(
        {
            "in": {
                "mode": mode, "entry": entry, "points": points, "canonical_valid": cvalid,
                "canonical_error": cerr, "lifecycle": lifecycle, "runtime_fp": FPS[r] or "",
                "baseline_fp": FPS[b] or "", "canonical_fp": FPS[c] or "", "state_error": serr,
                "force": force, "offline": offline,
            },
            "out": op_dict(op),
        }
    )

plan = build_skill_plan(Path("/ws"), "proj", [plan_single_skill(T, DesiredSkill("alpha", "copy", Path("/ws/skills/alpha"), "v1:ccc"),
    ObservedSkill(target=T, entry_type="dir", runtime_fingerprint="v1:aaa", canonical_fingerprint="v1:ccc",
                  state_record=SkillStateRecord("alpha", "copy", "active", "v1:bbb", "write", True), state_revision=3), force=True)])
authorization = plan.authorizations[0]

link_cases = []
for (mode, entry, points, same, cvalid, cerr, kind, scope, avail, parent, state, legacy, name) in itertools.product(
    ["link", "absent"],
    ["missing", "dir", "symlink", "file", "unsupported"],
    [False, True],
    [False, True],
    [True, False],
    ["", "bad"],
    ["managed_entry", "consumer_link", "instruction_link", "managed_container", "memory_link"],
    ["project", "global"],
    ["installed", "not_installed", "unknown"],
    [True, False],
    [False, True],
    [False, True],
    ["", "alpha"],
):
    if cvalid and cerr:
        continue
    if entry != "symlink" and points:
        continue
    if kind not in ("consumer_link", "instruction_link") and (avail != "installed" or not parent):
        continue
    if legacy and kind == "managed_container":
        continue
    observed = ObservedLink(
        target_path=Path("/t/target"),
        entry_type=entry,
        expected_canonical=Path("/c/canon"),
        canonical_valid=cvalid,
        canonical_error=cerr or None,
        raw_link_target=Path("/x/raw") if entry == "symlink" else None,
        resolved_link_target=Path("/x/resolved") if entry == "symlink" else None,
        link_points_to_canonical=points,
        is_same_object=same,
        target_kind=kind,
        scope=scope,
    )
    op = plan_link_target(observed, desired_mode=mode, availability_status=avail, parent_exists=parent,
                          has_state_record=state, is_legacy_container=legacy, resource_name=name)
    link_cases.append(
        {
            "in": {
                "mode": mode, "entry": entry, "points": points, "same": same, "canonical_valid": cvalid,
                "canonical_error": cerr, "kind": kind, "scope": scope, "avail": avail, "parent": parent,
                "state": state, "legacy": legacy, "name": name,
            },
            "out": {
                "action": op.action, "rule_id": op.rule_id, "reason": op.reason, "finding": op.finding or "",
                "is_authorized": op.is_authorized, "expected_representation": op.expected_representation,
                "desired_representation": op.desired_representation,
                "requires_parent_creation": op.requires_parent_creation, "is_same_object": op.is_same_object,
                "target_kind": op.target_kind, "resource_name": op.resource_name,
            },
        }
    )

# Directory fingerprints: build fixed trees in a temp dir.
fingerprints = []
TREES = {
    "single": {"SKILL.md": ("# hi\n", 0o644)},
    "nested_exec_empty": {
        "SKILL.md": ("x", 0o644),
        "bin/run.sh": ("#!/bin/sh\necho hi\n", 0o755),
        "docs/": None,
        "a/b/c.txt": ("deep", 0o600),
        ".aikito-executable.json": ("{}", 0o644),
    },
    "unicode": {"née.md": ("café\n", 0o644), "z.md": ("", 0o644)},
}
for name, files in TREES.items():
    root = Path(tempfile.mkdtemp())
    for rel, spec in files.items():
        if spec is None:
            (root / rel).mkdir(parents=True, exist_ok=True)
            continue
        p = root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(spec[0], encoding="utf-8")
        os.chmod(p, spec[1])
    fp, err = calculate_directory_fingerprint(root)
    fingerprints.append({
        "name": name,
        "files": {k: (None if v is None else {"content": v[0], "mode": v[1]}) for k, v in files.items()},
        "fingerprint": fp,
        "error": err,
    })

binding = {
    "workspace_root": "/nonexistent-ws/aikito",
    "project_name": " proj ",
    "checkout": "/nonexistent-co/p1",
    "hash": get_binding_hash(Path("/nonexistent-ws/aikito"), " proj ", Path("/nonexistent-co/p1")),
}

doc = ProjectSkillStateDocument(
    version=1, revision=4, workspace_root="/ws/aikito", project_name="proj", physical_checkout="/co/pé",
    records={
        "zeta": SkillStateRecord("zeta", "copy", "inactive", "v1:z", "claim", False),
        "alpha": SkillStateRecord("alpha", "copy", "active", "v1:a", "write", True),
    },
)
state_json = json.dumps(doc.to_dict(), indent=2, sort_keys=True)

def distinct_outcomes(cases):
    """Keep one input per distinct output: every behaviour, a small file."""
    seen = set()
    kept = []
    for case in cases:
        key = json.dumps(case["out"], sort_keys=True)
        if key not in seen:
            seen.add(key)
            kept.append(case)
    return kept


skill_cases = distinct_outcomes(skill_cases)
link_cases = distinct_outcomes(link_cases)

out = {
    "skill_cases": skill_cases,
    "authorization": authorization,
    "link_cases": link_cases,
    "fingerprints": fingerprints,
    "binding": binding,
    "state_json": state_json,
}
Path(__file__).with_name("vectors.json").write_text(json.dumps(out, indent=1, sort_keys=True) + "\n", encoding="utf-8")
print(f"{len(skill_cases)} skill cases, {len(link_cases)} link cases, sha256 "
      f"{hashlib.sha256(json.dumps(out, sort_keys=True).encode()).hexdigest()[:12]}")
