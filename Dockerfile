# syntax=docker/dockerfile:1

# Build both binaries from source. CGO stays off because the SQLite driver is
# pure Go (glebarez/modernc) — that is what lets the runtime stage below be a
# bare alpine with no toolchain and no libc surprises.
FROM golang:1.25-alpine AS build

WORKDIR /src

# Dependencies first: go.mod/go.sum change far less often than the source, so
# this layer survives most rebuilds.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# templ output (*_templ.go) is committed, so the build runs no code generation.
# -trimpath keeps host paths out of the binary; -s -w drops the symbol table.
# ragctl ships alongside the gateway because keys are CLI-only: without it a
# running container has no way to mint the first API key.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/gateway ./cmd/gateway && \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/ragctl  ./cmd/ragctl

FROM alpine:3

# ca-certificates for an HTTPS upstream, tzdata because monthly budgets reset on
# a local-time calendar boundary, and busybox wget is what HEALTHCHECK calls.
RUN apk add --no-cache ca-certificates tzdata

# Unprivileged: the process needs nothing beyond its own data directory.
RUN adduser -D -u 10001 app

COPY --from=build /out/gateway /out/ragctl /usr/local/bin/

# The SQLite file lives on a volume rather than in the image, so keys and usage
# survive a rebuild. WORKDIR is /data as well, which means a .env dropped in the
# volume is picked up by config.Load() the same way it is outside a container.
RUN mkdir -p /data && chown app:app /data
VOLUME /data
ENV DB_PATH=/data/ragembed.db
WORKDIR /data

USER app
EXPOSE 8080

# /healthz is the gateway's own liveness probe: it reports that the process is
# serving and deliberately does not touch the DB or the upstream, so it never
# flaps. The port is hardcoded here — override LISTEN_ADDR and this check needs
# the same edit.
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1

ENTRYPOINT ["gateway"]
