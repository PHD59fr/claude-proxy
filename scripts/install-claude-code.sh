#!/usr/bin/env bash
#
# Install Claude Code CLI (Node.js required).
#
# Usage:
#   ./scripts/install-claude-code.sh
#

set -euo pipefail

echo "Installing Claude Code..."

if ! command -v node &>/dev/null; then
    echo "Error: Node.js is required but not found."
    echo "Install Node.js: https://nodejs.org/"
    exit 1
fi

if ! command -v npm &>/dev/null; then
    echo "Error: npm is required but not found."
    exit 1
fi

npm install -g @anthropic-ai/claude-code

echo ""
echo "Claude Code installed. Run:"
echo "  ./scripts/claude-proxy.sh big-pickle"
