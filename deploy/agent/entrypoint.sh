#!/bin/sh
# Writes the Taskloom MCP server entry from the environment, then starts AgentKit.
set -eu
: "${TASKLOOM_MCP_URL:?set TASKLOOM_MCP_URL (see deploy/agent/issue-credential.sh)}"
: "${TASKLOOM_AUTHORIZATION:?set TASKLOOM_AUTHORIZATION (see deploy/agent/issue-credential.sh)}"
: "${OPENAI_API_KEY:?set OPENAI_API_KEY in deploy/agent/.env}"
umask 077
mkdir -p "$HOME/.agentkit"
cat > "$HOME/.agentkit/mcp.json" <<JSON
{
  "mcpServers": {
    "taskloom": {
      "transport": "streamable-http",
      "url": "$TASKLOOM_MCP_URL",
      "headers": {"Authorization": "$TASKLOOM_AUTHORIZATION"},
      "prefix": "taskloom__",
      "timeoutSeconds": 60
    }
  }
}
JSON
exec agentkit -base /app/config.base.yaml -config /app/agentkit.yaml "$@"
