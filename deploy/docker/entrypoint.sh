#!/bin/sh
# Writes the deployment overlay (the second and last config layer) from the
# environment, then starts Taskloom. A mounted /app/config.yaml is kept as is.
#   TASKLOOM_PORT    port to listen on, also published on the host (default 8899)
#   TASKLOOM_ORIGIN  exact browser origin (default http://localhost:$TASKLOOM_PORT)
# Plain http is accepted only for a loopback origin such as http://localhost;
# serve any other host over HTTPS.
set -eu
PORT="${TASKLOOM_PORT:-8899}"
ORIGIN="${TASKLOOM_ORIGIN:-http://localhost:$PORT}"
case "$PORT" in ''|*[!0-9]*) echo "TASKLOOM_PORT must be a number" >&2; exit 1 ;; esac
if [ ! -f /app/config.yaml ] || [ -f /app/.generated-config ]; then
  cat > /app/config.yaml <<EOF
http:
  config:
    listen: 0.0.0.0:$PORT
    origin: $ORIGIN
sql:
  config:
    dsn: /data/taskloom.db
blobs:
  config:
    dir: /data/files
EOF
  touch /app/.generated-config
fi
if [ "$#" -gt 0 ]; then exec "$@"; fi
exec taskloom -base /app/base.yaml -config /app/config.yaml
