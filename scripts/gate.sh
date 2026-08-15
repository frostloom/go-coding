#!/usr/bin/env bash
# Mock gate: run the full deterministic case set (12/12) as a go test.
# This is the automated guard behind the project's "CI-safe" claim — no
# network, no tokens, byte-deterministic. Daily `go test ./...` skips it
# (build-tagged); run this explicitly before committing / in CI.
#
#   scripts/gate.sh
set -euo pipefail

cd "$(dirname "$0")/.."

echo "== mock gate (full case set, deterministic) =="
go test -tags gate ./internal/harness -run TestMockGateAllCases -count=1 -v
