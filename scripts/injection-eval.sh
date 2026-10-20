#!/usr/bin/env bash
# Runs the prompt-injection eval against the real Claude model
# (docs/decisions.md, D058). It is paid: about 70 calls, roughly 0.20 US
# dollars a run at Sonnet 5.5's prices. make test never runs it.
#
# The key comes from the environment or from .env, which is loaded the way
# scripts/dev.sh loads it and never printed.
set -euo pipefail
cd "$(dirname "$0")/.."

if [ -f .env ]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
  echo "injection-eval: ANTHROPIC_API_KEY is not set (in the environment or .env); nothing was run." >&2
  exit 1
fi

out="$PWD/.data/evals"
mkdir -p "$out"
echo "injection-eval: model ${LLM_MODEL:-claude-sonnet-5-5}, 13 attacks and 1 control, 3 runs each, the 10 choices attacks also on the old prompt."
echo "injection-eval: about 70 paid calls, roughly 0.20 US dollars. The report goes to .data/evals/."

cd server
INJECTION_EVAL=1 INJECTION_EVAL_OUT="$out" \
  go test ./internal/providers/anthropic/ -run '^TestInjectionEval$' -count=1 -v -timeout 15m
