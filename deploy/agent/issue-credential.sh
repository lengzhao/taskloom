#!/bin/sh
# Issues a Taskloom credential for the AgentKit executor inside the
# running taskloom container and writes deploy/agent/taskloom.env (git-ignored).
# The secret is never printed. Each credential identifies one running instance;
# re-run to rotate it (the old executor is fenced on its next claim).
set -eu
CONTAINER=${CONTAINER:-taskloom}
TENANT=${TENANT:-example}
SUBJECT=${SUBJECT:-worker-1}
OWNER=${OWNER:-admin}
TTL=${TTL:-720h}
MCP_URL=${TASKLOOM_MCP_URL:-http://taskloom:8899/v1/mcp}
dir=$(cd "$(dirname "$0")" && pwd)
file=/data/agentkit-credential.json

docker exec "$CONTAINER" rm -f "$file"
docker exec -w /app "$CONTAINER" taskloom-admin -mode enroll-agent -tenant "$TENANT" -subject "$SUBJECT" -owner "$OWNER" \
  -ttl "$TTL" \
  -operator "${OPERATOR:-local}" -reason "AgentKit executor container" -credential-file "$file" >/dev/null
secret=$(docker exec "$CONTAINER" cat "$file" | sed -n 's/.*"Secret": *"\([^"]*\)".*/\1/p')
[ -n "$secret" ] || { echo "credential file has no Secret" >&2; exit 1; }
umask 077
printf 'TASKLOOM_MCP_URL=%s\nTASKLOOM_AUTHORIZATION=Bearer %s\n' "$MCP_URL" "$secret" > "$dir/taskloom.env"
echo "wrote $dir/taskloom.env for $SUBJECT ($TTL)"
