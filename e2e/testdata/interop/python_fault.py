"""Run the reference aikito CLI, but exit with status 137 (as SIGKILL would)
right after the n-th time a fault point is reached, leaving a transaction
half-done. Mirrors internal/faultinject's AIKITO_FAULT=<point>:<n>:

  skill-journal     after skill_runtime.write_transaction_journal succeeds
  workspace-rename  after each os.replace in workspace/transactions.py

Used only by the interop golden generator (e2e/generate_interop_test.go).
Run with PYTHONUNBUFFERED=1 so output printed before the exit isn't lost.
"""
import os
import sys
import types

point, count = os.environ["AIKITO_FAULT"].split(":")
limit = int(count)
seen = 0


def hit():
    global seen
    seen += 1
    if seen == limit:
        os._exit(137)


if point == "skill-journal":
    import aikito.skill_runtime as runtime

    original = runtime.write_transaction_journal

    def write_transaction_journal(*args, **kwargs):
        result = original(*args, **kwargs)
        if result[1] is None:
            hit()
        return result

    runtime.write_transaction_journal = write_transaction_journal
elif point == "workspace-rename":
    import aikito.workspace.transactions as transactions

    def replace(src, dst, *args, **kwargs):
        os.replace(src, dst, *args, **kwargs)
        hit()

    patched = types.ModuleType("os")
    patched.__dict__.update(os.__dict__)
    patched.replace = replace
    transactions.os = patched
else:
    raise SystemExit(f"unknown fault point {point!r}")

from aikito.cli import main  # noqa: E402

sys.argv = ["aikito", *sys.argv[1:]]
main()
