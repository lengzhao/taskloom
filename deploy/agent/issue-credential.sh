#!/bin/sh
# Issues a Taskloom business credential for the AgentKit executor inside the
# running taskloom container and writes deploy/agent/taskloom.env (git-ignored).
# The secret is never printed. Re-run to rotate it.
set -eu
CONTAINER=${CONTAINER:-taskloom}
TENANT=${TENANT:-example}
SUBJECT=${SUBJECT:-worker-1}
OWNER=${OWNER:-admin}
INSTANCE=${INSTANCE:-agentkit-docker}
INCARNATION=${INCARNATION:-boot-1}
TTL=${TTL:-720h}
MCP_URL=${TASKLOOM_MCP_URL:-http://taskloom:8899/v1/mcp}
dir=$(cd "$(dirname "$0")" && pwd)
file=/data/agentkit-business.json

docker exec "$CONTAINER" rm -f "$file"
docker exec -w /app "$CONTAINER" taskloom-admin -mode enroll-agent -tenant "$TENANT" -subject "$SUBJECT" -owner "$OWNER" \
  -instance "$INSTANCE" -incarnation "$INCARNATION" -purpose business -ttl "$TTL" \
  -operator "${OPERATOR:-local}" -reason "AgentKit executor container" -credential-file "$file" >/dev/null
secret=$(docker exec "$CONTAINER" cat "$file" | sed -n 's/.*"Secret": *"\([^"]*\)".*/\1/p')
[ -n "$secret" ] || { echo "credential file has no Secret" >&2; exit 1; }
umask 077
printf 'TASKLOOM_MCP_URL=%s\nTASKLOOM_AUTHORIZATION=Bearer %s\n' "$MCP_URL" "$secret" > "$dir/taskloom.env"
echo "wrote $dir/taskloom.env for $SUBJECT ($INSTANCE/$INCARNATION, $TTL)"
