# Envoryx Go runtime image: official golang image plus air (live reload), delve (the
# debugger GoLand and VS Code attach to), gotestsum (go test with a JUnit report for the
# Tests tab), git and socat. Runs idle (sleep) as a tooling
# container; the application server is started as the container's main process when the
# project enables it.
#
# Build: docker build --build-arg BASE_TAG=1.27-bookworm -t ghcr.io/envoryx/envoryx-go:1.27 images/go
ARG BASE_TAG=1.27-bookworm
FROM golang:${BASE_TAG}

ARG GO_VERSION
LABEL org.opencontainers.image.title="Envoryx Go ${GO_VERSION}" \
      org.opencontainers.image.source="https://github.com/envoryx/envoryx" \
      envoryx.runtime="go" \
      envoryx.runtime.version="${GO_VERSION}"

# air and delve are pinned to a release series like the base image tag, so a rebuild of
# an unchanged version does not silently ship different tools; bump them deliberately.
# They go to /usr/local/bin so any uid finds them; the module and build caches of the
# install are dropped again.
ARG AIR_VERSION=v1.67
ARG DELVE_VERSION=v1.27
ARG GOTESTSUM_VERSION=v1.13
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends git openssh-client ca-certificates socat procps \
        libfreetype6 fontconfig libxi6 libxrender1 libxtst6 libxext6; \
    rm -rf /var/lib/apt/lists/*; \
    GOBIN=/usr/local/bin go install "github.com/air-verse/air@${AIR_VERSION}"; \
    GOBIN=/usr/local/bin go install "github.com/go-delve/delve/cmd/dlv@${DELVE_VERSION}"; \
    GOBIN=/usr/local/bin go install "gotest.tools/gotestsum@${GOTESTSUM_VERSION}"; \
    go clean -cache -modcache; \
    go version; air -v; dlv version; gotestsum --version

# The containers run as the project's uid, which has no home here: the caches go to the
# shared package cache Envoryx mounts (GOMODCACHE, GOCACHE are set per container), and
# GOPATH/bin stays writable for `go install` of project tools.
ENV GOTOOLCHAIN=local \
    CGO_ENABLED=1

WORKDIR /var/www/html
CMD ["sleep", "infinity"]
