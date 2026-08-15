#!/usr/bin/env bash
# Live run of the go-coding agent against a real LLM (gpt-5.6-luna).
# Usage: scripts/run-live.sh <repo-dir> "<requirement>"
#
# The API key is read from environment (never committed):
#   export OPENAI_API_KEY=sk-...
# Set a default here only for local convenience.
set -euo pipefail

cd "$(dirname "$0")/.."

REPO="${1:-testdata/cases/fix_add}"
REQ="${2:-The Add function is broken. Fix it so it returns the correct sum of its two integer arguments.}"

if [ -z "${OPENAI_API_KEY:-}" ]; then
  echo "error: OPENAI_API_KEY is not set" >&2
  exit 1
fi

export OPENAI_BASE_URL="${OPENAI_BASE_URL:-https://sudobug.top/v1}"
export OPENAI_MODEL="${OPENAI_MODEL:-gpt-5.6-luna}"
export LLM_STREAM="${LLM_STREAM:-false}"

echo "== live run: repo=$REPO model=$OPENAI_MODEL =="
go run ./cmd/agent -repo "$REPO" -req "$REQ" -max-rounds 4 -run-timeout 120s -workdir .agent-workspace
