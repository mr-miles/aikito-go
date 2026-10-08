#!/bin/sh
# Regenerate expected mcps/*.toml by running the reference Python adopt on
# input/.claude.json. Run from the repo root.
set -eu
d=internal/cli/testdata/adopt_mcp_secrets
H=$(mktemp -d); mkdir -p "$H/.claude"; cp "$d/input/.claude.json" "$H/.claude.json"
export HOME="$H" PATH=/usr/bin:/bin PYTHONPATH="$PWD/../aikito/src"
(cd "$H" && python3 -m aikito init workspace >/dev/null && python3 -m aikito adopt >/dev/null)
rm -rf "$d/want"; mkdir -p "$d/want"; cp "$H"/aikito/mcps/*.toml "$d/want/"
