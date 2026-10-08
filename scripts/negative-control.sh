#!/bin/bash
# negative-control.sh <commit> [base]: run the tests and testdata that
# <commit> added or changed against the code of <base> (default <commit>^).
# Each affected package should FAIL; "PASSES on old code" means the change
# has no test that would catch its removal.
set -u
REPO=$(git rev-parse --show-toplevel); C=$1; P=${2:-$1^}
W=$(mktemp -d)/wt
git -C $REPO worktree add -q --detach $W $P
cd $W
# test-ish files changed in C (added/modified), relative to P
files=$(git -C $REPO diff --name-only --diff-filter=AMR $P $C | grep -E '(_test\.go$|/testdata/|^e2e/)')
for f in $files; do mkdir -p $(dirname $f); git -C $REPO show $C:$f > $f; done
pkgs=$(for f in $files; do d=$(dirname $f); while [ ! -e "$d" ] || ! ls $d/*.go >/dev/null 2>&1; do d=$(dirname $d); [ "$d" = . ] && break; done; echo ./$d; done | sed -E 's#/testdata.*##' | sort -u)
echo "## $C vs code of $P; packages: $(echo $pkgs)"
for p in $pkgs; do
  tags=""; [[ $p == ./e2e* ]] && tags="-tags e2e"
  out=$(go test -count=1 $tags $p 2>&1)
  if echo "$out" | grep -q "^ok"; then echo "  $p: PASSES on old code (no failing test!)"
  elif echo "$out" | grep -qE "build failed|setup failed|undefined:|cannot use"; then echo "  $p: COMPILE FAILURE on old code:"; echo "$out" | grep -E "undefined|cannot|error" | head -4 | sed 's/^/      /'
  else echo "  $p: FAILS on old code:"; echo "$out" | grep -E "^\s*--- FAIL" | sed 's/^ */      /' | sort -u | head -25; fi
done
cd /; git -C $REPO worktree remove --force $W
