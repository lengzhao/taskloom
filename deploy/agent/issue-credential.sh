#!/bin/sh
# Issues a Taskloom credential for an AgentKit executor (presets/taskloom.yaml
# in github.com/lengzhao/agentkit) inside the running taskloom container, and
# writes two git-ignored files next to this script without printing the secret:
#   taskloom.env  TASKLOOM_API_URL and TASKLOOM_AUTHORIZATION for platform/taskloom
#   mcp.json      the Taskloom MCP server entry for ~/.agentkit/mcp.json
# Each credential identifies one running instance; re-run to rotate it (the old
# executor is fenced on its next claim).
set -eu
CONTAINER=${CONTAINER:-taskloom}
TENANT=${TENANT:-example}
SUBJECT=${SUBJECT:-worker-1}
OWNER=${OWNER:-admin}
TTL=${TTL:-720h}
API_URL=${TASKLOOM_API_URL:-http://127.0.0.1:8899/v1}
dir=$(cd "$(dirname "$0")" && pwd)
file=/data/agentkit-credential.json

docker exec "$CONTAINER" rm -f "$file"
docker exec -w /app "$CONTAINER" taskloom-admin -mode enroll-agent -tenant "$TENANT" -subject "$SUBJECT" -owner "$OWNER" \
  -ttl "$TTL" \
  -operator "${OPERATOR:-local}" -reason "AgentKit executor" -credential-file "$file" >/dev/null
secret=$(docker exec "$CONTAINER" cat "$file" | sed -n 's/.*"Secret": *"\([^"]*\)".*/\1/p')
docker exec "$CONTAINER" rm -f "$file"
[ -n "$secret" ] || { echo "credential file has no Secret" >&2; exit 1; }
umask 077
# Quoted so the file can be sourced by a POSIX shell (the value has a space).
printf "TASKLOOM_API_URL='%s'\nTASKLOOM_AUTHORIZATION='Bearer %s'\n" "$API_URL" "$secret" > "$dir/taskloom.env"
cat > "$dir/mcp.json" <<JSON
{
  "mcpServers": {
    "taskloom": {
      "transport": "streamable-http",
      "url": "$API_URL/mcp",
      "headers": {"Authorization": "Bearer $secret"},
      "prefix": "taskloom__",
      "timeoutSeconds": 60
    }
  }
}
JSON
echo "wrote $dir/taskloom.env and $dir/mcp.json for $SUBJECT ($TTL)"
