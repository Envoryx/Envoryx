# Envoryx Node.js runtime image: official node image plus corepack-activated pnpm and yarn,
# git for tooling that needs it. Runs idle (sleep) as a tooling container; commands are
# executed through Envoryx actions and the terminal.
#
# Build: docker build --build-arg BASE_TAG=24-bookworm-slim -t ghcr.io/envoryx/envoryx-node:24 images/node
ARG BASE_TAG=24-bookworm-slim
FROM node:${BASE_TAG}

ARG NODE_VERSION
LABEL org.opencontainers.image.title="Envoryx Node ${NODE_VERSION}" \
      org.opencontainers.image.source="https://github.com/envoryx/envoryx" \
      envoryx.runtime="node" \
      envoryx.runtime.version="${NODE_VERSION}"

# Set before the build step: `pnpm --version` below fetches pnpm's native binary into
# COREPACK_HOME, and only there does it reach the image.
ENV COREPACK_HOME=/usr/local/lib/corepack \
    COREPACK_ENABLE_DOWNLOAD_PROMPT=0 \
    NPM_CONFIG_UPDATE_NOTIFIER=false

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends git openssh-client ca-certificates socat procps libfreetype6 fontconfig libxi6 libxrender1 libxtst6 libxext6; \
    rm -rf /var/lib/apt/lists/*; \
    # Node 25+ no longer bundles corepack and old images ship a corepack that cannot verify
    # current registry signatures: always install the latest one.
    npm install -g corepack@latest; \
    corepack enable; \
    # Pre-activate current pnpm and yarn so they work offline.
    corepack prepare pnpm@latest --activate; \
    corepack prepare yarn@stable --activate; \
    node --version; npm --version; pnpm --version; yarn --version; \
    # The containers run as the project's uid, which the image cannot know: Corepack must
    # be able to add the version a project pins in "packageManager" (and pnpm its native
    # binary) for any uid.
    chmod -R a+rwX "$COREPACK_HOME"

WORKDIR /var/www/html
CMD ["sleep", "infinity"]
