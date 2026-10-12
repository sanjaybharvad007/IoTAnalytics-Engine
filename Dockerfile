# syntax=docker/dockerfile:1.7
# Multi-stage build: compile a static Go binary, ship it on a distroless base
# (no shell, no package manager, runs as non-root). Final image is ~10 MB.

ARG GO_VERSION=1.24

# ---- build -----------------------------------------------------------------
FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
# Cache module downloads separately from source changes.
COPY go.mod ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG CMD=server
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/app ./cmd/${CMD}

# ---- runtime ---------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
LABEL org.opencontainers.image.title="IoTAnalytics-Engine" \
      org.opencontainers.image.description="Real-time IoT fleet analytics: sliding windows, safety alerts, geofencing, durable event log, SSE + Prometheus" \
      org.opencontainers.image.source="https://github.com/sanjaybharvad007/IoTAnalytics-Engine" \
      org.opencontainers.image.licenses="MIT"
WORKDIR /
COPY --from=build /out/app /app
# Durable event log lives on a volume.
VOLUME ["/data"]
ENV IOT_ADDR=:8080 \
    IOT_DATA=/data/ops.log \
    IOT_LOG_FORMAT=json
EXPOSE 8080
USER nonroot:nonroot
# The binary probes itself: distroless has no curl/wget.
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/app", "-healthcheck"]
ENTRYPOINT ["/app"]
