# Container image for sos-beacon (the topic-agnostic signal harvester).
# Multi-stage: build the static Go binary, then ship it on a distroless base.
# Built + pushed to Docker Hub (immersivefusion/sos-beacon) by
# .github/workflows/release.yml on a version-tag push. Multi-arch via buildx
# (TARGETOS/TARGETARCH).
#
# Usage (mounts a config + state file, reads secrets from the environment):
#   docker run --rm \
#     -e OAI_KEY -e OAI_BASE_URL -e SOS_APM_WEBHOOK \
#     -v "$PWD/config.yaml:/config.yaml:ro" \
#     -v "$PWD/sos-state.json:/sos-state.json" \
#     immersivefusion/sos-beacon -config /config.yaml
# syntax=docker/dockerfile:1

FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o /out/sos-beacon ./cmd/sos-beacon

# distroless/static: no shell, CA certs included (for HTTPS egress to HN /
# Discord / the LLM API), runs as non-root.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/sos-beacon /usr/bin/sos-beacon
# Containers run permanently, so default to errors-only: the per-run stats line
# is log noise at that cadence. The bare CLI default stays "info". Override with
# -e SOS_BEACON_LOG_LEVEL=info|debug, or pass -log-level.
ENV SOS_BEACON_LOG_LEVEL=error
ENTRYPOINT ["/usr/bin/sos-beacon"]
