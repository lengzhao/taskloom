# AgentKit runtime image for the resident Taskloom executor Agent.
# Build from the repository root:
#   docker build --build-arg GOPROXY="$(go env GOPROXY)" --build-arg GOSUMDB="$(go env GOSUMDB)" \
#     -f deploy/agent/Dockerfile -t taskloom-agent:local deploy/agent
FROM golang:1.27.3-alpine AS build
ARG GOPROXY
ARG GOSUMDB
ARG GOPRIVATE
ARG AGENTKIT_VERSION=v0.4.45
ENV GOPROXY=${GOPROXY} GOSUMDB=${GOSUMDB} GOPRIVATE=${GOPRIVATE} CGO_ENABLED=0
RUN go install -trimpath -ldflags="-s -w" github.com/lengzhao/agentkit/cmd/agent@${AGENTKIT_VERSION} \
 && mkdir -p /out \
 && cp /go/bin/agent /out/agentkit \
 && cp "$(go env GOMODCACHE)/github.com/lengzhao/agentkit@${AGENTKIT_VERSION}/config.base.yaml" /out/config.base.yaml

FROM alpine:3.23
RUN apk add --no-cache ca-certificates \
 && adduser -D -u 10002 agent
COPY --from=build /out/agentkit /usr/local/bin/agentkit
WORKDIR /app
COPY --from=build /out/config.base.yaml ./config.base.yaml
COPY agentkit.yaml ./agentkit.yaml
COPY entrypoint.sh /usr/local/bin/taskloom-agent
USER agent
ENV HOME=/home/agent AGENTKIT_LOG_LEVEL=info
WORKDIR /home/agent
ENTRYPOINT ["taskloom-agent"]
