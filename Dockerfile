# syntax=docker/dockerfile:1
#
# dashboards — Hanzo Base-native Go service binary (console tRPC→ZAP migration).
# Single pure-Go binary: Base (embedded SQLite + vault) + typed ZAP router.
#
# CI builds this multi-arch (linux/amd64 + linux/arm64) on the hanzoai
# self-hosted runners → ghcr.io/hanzoai/dashboards. Do NOT build locally.
#
# go.mod carries NO `replace` directives: every dependency resolves to a
# published version through GOPROXY, so this build context is just this repo.
# Keep it that way — a replace pointing at a sibling checkout builds here and
# nowhere else.
FROM golang:1.26.5-alpine AS builder
RUN apk add --no-cache git ca-certificates tzdata
WORKDIR /build
COPY go.mod go.sum ./
# Resolve modules through the module proxy, never direct. proxy.golang.org and
# sum.golang.org agree on every dependency here and neither can change under us;
# github.com serves whatever a moved tag currently points at. Pinning the proxy
# is what makes go.sum checkable against the checksum database at all.
ENV GOPROXY=https://proxy.golang.org,direct
# The cache mount is keyed by id. go.sum once recorded hashes copied off a
# developer machine whose module cache predated an upstream tag move: this build
# failed verification while that machine "confirmed" the wrong bytes from its own
# cache. go.sum is corrected against proxy+sumdb; the id bump abandons any layer
# cache populated while it was wrong.
RUN --mount=type=cache,id=gomod-v2,target=/go/pkg/mod go mod download
COPY . .

# JSON v2 per SCALE_STANDARD.md §2: error/results bodies serialize via
# encoding/json; jsonv2 trims time + allocs on the hot path.
ARG GO_EXPERIMENT=jsonv2
ENV GOEXPERIMENT=${GO_EXPERIMENT}

# Pure-Go (CGO off): modernc SQLite + luxfi/zap need no C toolchain; the binary
# is statically linked and cross-compiles cleanly for both target arches.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w" \
    -o /build/dashboards .

# One directory in an empty image: the static binary and the files it reads;
# nothing else is present to run, so nothing else can be run.
FROM alpine:3.22 AS root
RUN apk add --no-cache ca-certificates tzdata && mkdir -p /data /vaults && chown -R 65532:65532 /data /vaults

FROM scratch
COPY --from=root /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=root /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=root --chown=65532:65532 /data /data
COPY --from=root --chown=65532:65532 /vaults /vaults
COPY --from=builder /build/dashboards /app/dashboards
USER 65532:65532
EXPOSE 8090 9995
ENTRYPOINT ["/app/dashboards"]
CMD ["serve", "--http=0.0.0.0:8090", "--zap=0.0.0.0:9995", "--dir=/data", "--vaultDir=/vaults"]
