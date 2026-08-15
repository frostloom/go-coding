#!/usr/bin/env bash
# MCP server smoke demo: drives ONE live stdio process the way a host
# (VSCode Claude extension, Claude Desktop, ...) would — explore → edit
# preview → apply → run tests → show the cumulative diff.
#
# Usage: scripts/mcp-demo.sh [workdir]    (default: testdata/cases/fix_add)
set -euo pipefail

cd "$(dirname "$0")/.."

SRC="${1:-testdata/cases/fix_add}"
BIN="$(pwd)/bin/mcp-server.exe"
mkdir -p bin

# Go and git are not guaranteed on PATH on this machine.
# MSYS2 path format so both bash lookup AND the native server's own
# `go`/`git` spawns see them.
if ! command -v go >/dev/null 2>&1; then
  export PATH="/e/go1.25.12.windows-amd64/go/bin:$PATH"
fi
if ! command -v git >/dev/null 2>&1; then
  export PATH="/d/git/Git/mingw64/bin:$PATH"
fi
go build -o "$BIN" ./cmd/mcp-server

# Work on a COPY so the demo never mutates a committed fixture. Drop any
# stray .git that got copied along (a fixture must start with a CLEAN git
# baseline, otherwise EnsureRepo's first commit is a no-op and git_diff has
# nothing to show).
WORKDIR="$(mktemp -d)"
cp -r "$SRC/." "$WORKDIR/"
rm -rf "$WORKDIR/.git"
trap 'rm -rf "$WORKDIR"' EXIT

echo "== workspace: $SRC (copied to temp) =="

# One live server process, driven interactively (coproc keeps it alive across
# writes, exactly like a host's MCP client).
coproc MCP { "$BIN" -workdir "$WORKDIR"; }
IN="${MCP[1]}"; OUT="${MCP[0]}"

rpc() { printf '%s\n' "$1" >&"$IN"; }

# 1. explore
echo "== read_file mathops.go =="
rpc '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"mathops.go"}}}'
head -1 <&"$OUT"

# 2. edit preview (returns a pending id + diff)
echo "== edit_file (preview, NOT written) =="
rpc '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"edit_file","arguments":{"path":"mathops.go","old":"return a - b // bug: should be +","new":"return a + b"}}}'
PREVIEW="$(head -1 <&"$OUT")"
echo "$PREVIEW"
ID="$(echo "$PREVIEW" | grep -o 'pe-[a-f0-9-]*' | head -1)"

# 3. apply it (runs syntax + call-graph validation)
echo "== apply_edit $ID =="
rpc "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"apply_edit\",\"arguments\":{\"id\":\"$ID\"}}}"
head -1 <&"$OUT"

# 4. run tests
echo "== run_test =="
rpc '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"run_test","arguments":{}}}'
head -1 <&"$OUT"

# 5. cumulative diff since baseline
echo "== git_diff =="
rpc '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"git_diff","arguments":{}}}'
head -1 <&"$OUT"

exec {IN}>&-
wait 2>/dev/null || true
