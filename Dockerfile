# syntax=docker/dockerfile:1.7

# ---- Frontend -------------------------------------------------------------
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---- Backend --------------------------------------------------------------
FROM golang:1.27-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/staqio ./cmd/staqio

# ---- Runtime --------------------------------------------------------------
FROM alpine:3.21
LABEL org.opencontainers.image.title="Staqio" \
      org.opencontainers.image.description="Docker-native development environments for Unraid and Linux" \
      org.opencontainers.image.source="https://github.com/seramos/staqio" \
      org.opencontainers.image.licenses="AGPL-3.0-only"

RUN apk add --no-cache ca-certificates tzdata \
 && mkdir -p /config /projects

COPY --from=build /out/staqio /usr/local/bin/staqio

ENV STAQIO_LISTEN=:8787 \
    STAQIO_CONFIG_DIR=/config \
    STAQIO_PROJECTS_DIR=/projects \
    STAQIO_LOG_FORMAT=json \
    PUID=99 \
    PGID=100

VOLUME ["/config", "/projects"]
EXPOSE 8787

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["staqio", "healthcheck"]

# Staqio runs as root: it needs the Docker socket and adjusts ownership of project
# directories to PUID/PGID. See SECURITY.md for the implications and the socket-proxy option.
ENTRYPOINT ["staqio"]
CMD ["serve"]
