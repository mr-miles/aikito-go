#!/usr/bin/env bash
# Show what has changed in the upstream Python aikito since this port's
# baseline, and whether any files copied verbatim from upstream have drifted.
#
# Usage: scripts/upstream-diff.sh [UPSTREAM_CHECKOUT] [NEW_REF]
#   UPSTREAM_CHECKOUT  a clone of https://github.com/lsaint/aikito
#                      (default: ../aikito next to this repo)
#   NEW_REF            upstream ref to compare against (default: origin/main
#                      after a fetch)
#
# The baseline commit is read from UPSTREAM.md. See UPSTREAM.md for the full
# backport procedure.
set -euo pipefail

here=$(cd "$(dirname "$0")/.." && pwd)
upstream=${1:-"$here/../aikito"}
upstream=$(cd "$upstream" && pwd)
base=$(grep -oE '^- \*\*Commit:\*\* `[0-9a-f]{40}`' "$here/UPSTREAM.md" | grep -oE '[0-9a-f]{40}')

git -C "$upstream" fetch --quiet --tags origin || echo "(fetch failed; using local refs)" >&2
new=${2:-origin/main}
newsha=$(git -C "$upstream" rev-parse "$new")

echo "Baseline: $base ($(git -C "$upstream" describe --tags --always "$base"))"
echo "Compare:  $newsha ($(git -C "$upstream" describe --tags --always "$newsha"))"
echo

echo "== Upstream commits since baseline =="
git -C "$upstream" log --oneline --no-merges "$base..$newsha" || true
echo

echo "== Upstream files changed since baseline (src, tests, docs) =="
git -C "$upstream" diff --stat=200 "$base" "$newsha" -- src tests docs README.md LICENSE || true
echo

echo "== Verbatim copies that differ from upstream at $new =="
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
git -C "$upstream" archive "$newsha" src/aikito/templates docs LICENSE | tar -x -C "$tmp"
t="$tmp/src/aikito/templates"
drift=0
check() { # upstream_path local_path
	if ! diff -rq "$1" "$2" >/dev/null 2>&1; then
		echo "DRIFT: ${2#$here/}"
		diff -ru "$1" "$2" | head -40 || true
		drift=1
	fi
}
for p in config.toml gitignore global project skills skills.toml; do
	check "$t/$p" "$here/internal/cli/templates/$p"
done
check "$t/agents" "$here/internal/registry/templates/agents"
check "$tmp/LICENSE" "$here/LICENSE"
# docs/README.md is ours (port notes), not upstream's.
if ! diff -rq -x README.md "$tmp/docs" "$here/docs" >/dev/null 2>&1; then
	echo "DRIFT: docs/ (see: diff -ru -x README.md <upstream>/docs docs)"
	drift=1
fi
layout=$(cat "$t/layout.toml")
if [[ "$layout" != "version = 2" ]]; then
	echo "DRIFT: templates/layout.toml is now '$layout' (Go hard-codes LayoutContent in internal/workspace/layout.go)"
	drift=1
fi
[[ $drift == 0 ]] && echo "(none)"
