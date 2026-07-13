#!/usr/bin/env bash
#
# Launch Claude Code with OpenAI Codex backend via the proxy.
#
# Usage:
#   ./scripts/claude-codex.sh [model] [extra claude args...]
#
# Examples:
#   ./scripts/claude-codex.sh gpt-5.2-codex
#   ./scripts/claude-codex.sh gpt-5.1-codex-max --verbose
#
# The proxy must be running with codex tokens loaded
#

set -euo pipefail

MODEL="${1:-gpt-5.2-codex}"
shift 2>/dev/null || true

if ! command -v claude &>/dev/null; then
    echo "Error: 'claude' CLI not found in PATH."
    echo "Install it with: npm install -g @anthropic-ai/claude-code"
    exit 1
fi

if ! curl -sf http://127.0.0.1:3000/healthz >/dev/null 2>&1; then
    echo "Warning: proxy does not appear to be running on http://127.0.0.1:3000"
    echo "Start it with: go run ./cmd/claude-proxy serve"
    echo ""
fi

echo "Starting Claude Code with Codex model: $MODEL"
echo "Proxy: http://127.0.0.1:3000"
echo ""

export ANTHROPIC_BASE_URL="http://127.0.0.1:3000"
export ANTHROPIC_AUTH_TOKEN="unused"

export ANTHROPIC_MODEL="$MODEL"
export CLAUDE_CODE_SUBAGENT_MODEL="$MODEL"

export ANTHROPIC_DEFAULT_OPUS_MODEL="$MODEL"
export ANTHROPIC_DEFAULT_SONNET_MODEL="$MODEL"
export ANTHROPIC_DEFAULT_HAIKU_MODEL="$MODEL"
export ANTHROPIC_DEFAULT_FABLE_MODEL="$MODEL"

export CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1

unset ANTHROPIC_API_KEY

exec claude --model "$MODEL" "$@"
