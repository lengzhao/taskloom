# Taskloom single-container image: server + local admin CLI, SQLite under /data.
# The SQLite driver needs cgo, so build on Alpine with a C toolchain.
FROM golang:1.27.3-alpine AS build
# Module proxy settings are build arguments, e.g. pass the host's values:
#   docker build --build-arg GOPROXY="$(go env GOPROXY)" --build-arg GOSUMDB="$(go env GOSUMDB)" .
ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org
ARG GOPRIVATE=
ENV GOPROXY=${GOPROXY} GOSUMDB=${GOSUMDB} GOPRIVATE=${GOPRIVATE}
RUN apk add --no-cache gcc musl-dev
WORKDIR /src
COPY . .
# A vendor/ directory in the build context is used as-is (offline builds);
# otherwise modules are downloaded.
RUN if [ ! -f vendor/modules.txt ]; then go mod download; fi \
 && CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/taskloom ./cmd/taskloom \
 && CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/taskloom-admin ./cmd/taskloom-admin \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/taskloom-mcp ./cmd/taskloom-mcp

FROM alpine:3.23
RUN adduser -D -u 10001 taskloom && mkdir -p /data /app && chown taskloom /data /app
WORKDIR /app
COPY --from=build /out/taskloom /out/taskloom-admin /out/taskloom-mcp /usr/local/bin/
COPY base.yaml ./base.yaml
COPY deploy/docker/entrypoint.sh /usr/local/bin/taskloom-entrypoint
USER taskloom
VOLUME ["/data"]
# Publish the same port on the host: docker run -p 8899:8899 (or set TASKLOOM_PORT).
ENV TASKLOOM_PORT=8899 TASKLOOM_ORIGIN=
EXPOSE 8899
ENTRYPOINT ["taskloom-entrypoint"]
