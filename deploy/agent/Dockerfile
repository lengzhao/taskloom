# AgentKit runtime image for the resident Taskloom executor Agent: the stock
# AgentKit runner plus the platform/taskloom adapter (harness/agentkit).
# Build from the repository root (the adapter is compiled from this module):
#   docker build --build-arg GOPROXY="$(go env GOPROXY)" --build-arg GOSUMDB="$(go env GOSUMDB)" \
#     -f deploy/agent/Dockerfile -t taskloom-agent:local .
FROM golang:1.27.3-alpine AS build
ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org
ARG GOPRIVATE=
ENV GOPROXY=${GOPROXY} GOSUMDB=${GOSUMDB} GOPRIVATE=${GOPRIVATE} CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN if [ ! -f vendor/modules.txt ]; then go mod download; fi
COPY . .
# The AgentKit version and its L0 base config come from go.mod, so the adapter
# and the runtime it plugs into can never drift apart.
RUN mkdir -p /out \
 && go build -trimpath -ldflags="-s -w" -o /out/taskloom-agent ./cmd/taskloom-agent \
 && cp "$(go list -m -f '{{.Dir}}' github.com/lengzhao/agentkit)/config.base.yaml" /out/config.base.yaml

FROM alpine:3.23
RUN apk add --no-cache ca-certificates \
 && adduser -D -u 10002 agent
COPY --from=build /out/taskloom-agent /usr/local/bin/taskloom-agent
WORKDIR /app
COPY --from=build /out/config.base.yaml ./config.base.yaml
COPY deploy/agent/agentkit.yaml ./agentkit.yaml
COPY deploy/agent/entrypoint.sh /usr/local/bin/taskloom-agent-entrypoint
USER agent
ENV HOME=/home/agent AGENTKIT_LOG_LEVEL=info
WORKDIR /home/agent
ENTRYPOINT ["taskloom-agent-entrypoint"]
