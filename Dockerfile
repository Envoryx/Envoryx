# syntax=docker/dockerfile:1.7

# ---- Frontend -------------------------------------------------------------
# Build stages run on the builder's native platform; only the final stage is per-arch.
FROM --platform=$BUILDPLATFORM node:25-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---- Backend --------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/envoryx ./cmd/envoryx

# ---- Runtime --------------------------------------------------------------
FROM alpine:3.21
LABEL org.opencontainers.image.title="Envoryx" \
      org.opencontainers.image.description="Docker-native development environments for Unraid and Linux" \
      org.opencontainers.image.source="https://github.com/envoryx/envoryx" \
      org.opencontainers.image.licenses="AGPL-3.0-only"

RUN apk add --no-cache ca-certificates tzdata \
 && mkdir -p /config /projects

COPY --from=build /out/envoryx /usr/local/bin/envoryx

ENV ENVORYX_LISTEN=:8787 \
    ENVORYX_CONFIG_DIR=/config \
    ENVORYX_PROJECTS_DIR=/projects \
    ENVORYX_LOG_FORMAT=json \
    PUID=99 \
    PGID=100

VOLUME ["/config", "/projects"]
EXPOSE 8787 80 443 2222

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["envoryx", "healthcheck"]

# Envoryx runs as root: it needs the Docker socket and adjusts ownership of project
# directories to PUID/PGID. See SECURITY.md for the implications and the socket-proxy option.
ENTRYPOINT ["envoryx"]
CMD ["serve"]
