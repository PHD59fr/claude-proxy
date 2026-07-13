#!/usr/bin/env bash
#
# Launch Claude Code with OpenCode free models via the proxy.
#
# Usage:
#   ./scripts/claude-proxy.sh [model] [extra claude args...]
#
# Examples:
#   ./scripts/claude-proxy.sh big-pickle
#   ./scripts/claude-proxy.sh deepseek-v4-flash-free --verbose
#
# The proxy must be running on http://127.0.0.1:3000
#

set -euo pipefail

MODEL="${1:-big-pickle}"
shift 2>/dev/null || true

# Check if claude is available
if ! command -v claude &>/dev/null; then
    echo "Error: 'claude' CLI not found in PATH."
    echo "Install it with: npm install -g @anthropic-ai/claude-code"
    exit 1
fi

# Check if proxy is running
if ! curl -sf http://127.0.0.1:3000/healthz >/dev/null 2>&1; then
    echo "Warning: proxy does not appear to be running on http://127.0.0.1:3000"
    echo "Start it with: go run ./cmd/claude-proxy serve"
    echo ""
fi

echo "Starting Claude Code with model: $MODEL"
echo "Proxy: http://127.0.0.1:3000"
echo ""

# Configure Claude Code to use our proxy
export ANTHROPIC_BASE_URL="http://127.0.0.1:3000"
export ANTHROPIC_AUTH_TOKEN="unused"

export ANTHROPIC_MODEL="$MODEL"
export CLAUDE_CODE_SUBAGENT_MODEL="$MODEL"

export ANTHROPIC_DEFAULT_OPUS_MODEL="$MODEL"
export ANTHROPIC_DEFAULT_SONNET_MODEL="$MODEL"
export ANTHROPIC_DEFAULT_HAIKU_MODEL="$MODEL"
export ANTHROPIC_DEFAULT_FABLE_MODEL="$MODEL"

export CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1

# Do not forward the real Anthropic API key
unset ANTHROPIC_API_KEY

exec claude --model "$MODEL" "$@"
