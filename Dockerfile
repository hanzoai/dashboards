# syntax=docker/dockerfile:1
#
# dashboards — Hanzo Base-native Go service binary (console tRPC→ZAP migration).
# Single pure-Go binary: Base (embedded SQLite + vault) + typed ZAP router.
#
# CI builds this multi-arch (linux/amd64 + linux/arm64) on the hanzoai
# self-hosted runners → ghcr.io/hanzoai/dashboards. Do NOT build locally.
#
# NOTE on go.mod replaces: the working-copy go.mod carries `replace` directives
# to ../base and ../../zap-proto/go for local dev while those modules' proxy
# availability is flaky. CI builds against the PUBLISHED modules — the release
# pipeline drops the replaces (the modules resolve via GOPROXY) so this build
# context needs only this repo. If you must build with the replaces, use a
# monorepo build context that includes the sibling modules.
FROM golang:1.26.4-alpine AS builder
RUN apk add --no-cache git ca-certificates tzdata
WORKDIR /build
COPY go.mod go.sum ./
# Resolve modules through the module proxy, never direct. A luxfi tag was
# force-moved upstream, so github.com now serves different bytes than the
# immutable proxy copy that go.sum records — fetching direct fails verification
# with "SECURITY ERROR ... does NOT match an earlier download". The proxy is
# also the reproducible source: it cannot change under us the way a moved tag can.
ENV GOPROXY=https://proxy.golang.org,direct
# The cache mount is keyed by id. A luxfi tag was force-moved upstream, so a
# cache populated before the move holds bytes that no longer verify against
# go.sum and every later build fails with "SECURITY ERROR ... does NOT match an
# earlier download" — while a clean machine builds fine. Bumping the id
# abandons the poisoned cache instead of chasing the hash.
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

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata curl \
    && addgroup -S hanzo && adduser -S hanzo -G hanzo
WORKDIR /app
COPY --from=builder /build/dashboards /app/dashboards
RUN mkdir -p /data /vaults && chown -R hanzo:hanzo /app /data /vaults
USER hanzo
# 8090 = Base sidecar HTTP (health/metrics); 9995 = typed ZAP capability RPC.
EXPOSE 8090 9995
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8090/healthz || exit 1
ENTRYPOINT ["/app/dashboards"]
CMD ["serve", "--http=0.0.0.0:8090", "--zap=0.0.0.0:9995", "--dir=/data", "--vaultDir=/vaults"]
