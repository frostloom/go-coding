#!/usr/bin/env bash
# Run the full M5 evaluation set against a real LLM and write the baseline
# report to harness-out/live.json.
#
#   export OPENAI_API_KEY=sk-...           # key stays in the env, never committed
#   scripts/run-live-harness.sh
#
# The full 12/12 baseline attaches the MCP demo server (mcp-demo-server.exe,
# built here) as an external knowledge tool: fix_legacy_time's hidden wire
# layout lives only there, so -mcp is what makes it solvable. Set MCP=0 to run
# the 11/12 no-tool baseline instead.
#
# Optional env overrides:
#   OPENAI_BASE_URL / OPENAI_MODEL  — default https://sudobug.top/v1, gpt-5.6-luna
#   PRICE_PER_1M                    — USD per 1M tokens, prints per-case cost estimate
#   MCP                             — 1 (default) attach the demo server; 0 = no external tools
#   MAX_TOKENS_FIX/IMPL/SELF        — P14 per-type cost ceilings (0 = no gate).
#     Guardrail the run: a case of that type stops once it passes the ceiling
#     instead of burning further. Pick from the live baseline — self_codeindex
#     alone has run 8.6k-15k tokens, so a self ceiling must clear that.
set -euo pipefail

cd "$(dirname "$0")/.."

if [ -z "${OPENAI_API_KEY:-}" ]; then
  echo "error: OPENAI_API_KEY is not set" >&2
  exit 1
fi

export OPENAI_BASE_URL="${OPENAI_BASE_URL:-https://sudobug.top/v1}"
export OPENAI_MODEL="${OPENAI_MODEL:-gpt-5.6-luna}"
export LLM_STREAM="${LLM_STREAM:-false}"

ARGS=(-mode live -out harness-out/live.json -max-rounds 4 -run-timeout 120s)
if [ "${MCP:-1}" = "1" ]; then
  go build -o mcp-demo-server.exe ./cmd/mcp-demo-server
  ARGS+=(-mcp "compat ./mcp-demo-server.exe")
fi
if [ -n "${PRICE_PER_1M:-}" ]; then
  ARGS+=(-price-per-1m "${PRICE_PER_1M}")
fi
# P14 per-type cost budgets (default 0 = no gate, exactly the P13 baseline).
ARGS+=(-max-tokens-fix "${MAX_TOKENS_FIX:-0}" -max-tokens-impl "${MAX_TOKENS_IMPL:-0}" -max-tokens-self "${MAX_TOKENS_SELF:-0}")

mkdir -p harness-out
echo "== live harness: model=$OPENAI_MODEL cases=12 mcp=${MCP:-1} =="
go run ./cmd/harness "${ARGS[@]}"
