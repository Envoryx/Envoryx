# Staqio Node.js runtime image: official node image plus corepack-activated pnpm and yarn,
# git for tooling that needs it. Runs idle (sleep) as a tooling container; commands are
# executed through Staqio actions and the terminal.
#
# Build: docker build --build-arg BASE_TAG=24-bookworm-slim -t ghcr.io/seramos/staqio-node:24 images/node
ARG BASE_TAG=24-bookworm-slim
FROM node:${BASE_TAG}

ARG NODE_VERSION
LABEL org.opencontainers.image.title="Staqio Node ${NODE_VERSION}" \
      org.opencontainers.image.source="https://github.com/seramos/staqio" \
      staqio.runtime="node" \
      staqio.runtime.version="${NODE_VERSION}"

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends git openssh-client ca-certificates socat procps libfreetype6 fontconfig libxi6 libxrender1 libxtst6 libxext6; \
    rm -rf /var/lib/apt/lists/*; \
    # Node 25+ no longer bundles corepack and old images ship a corepack that cannot verify
    # current registry signatures: always install the latest one.
    npm install -g corepack@latest; \
    corepack enable; \
    # Pre-activate current pnpm and yarn so they work offline and for any uid.
    COREPACK_HOME=/usr/local/lib/corepack corepack prepare pnpm@latest --activate; \
    COREPACK_HOME=/usr/local/lib/corepack corepack prepare yarn@stable --activate; \
    chmod -R a+rX /usr/local/lib/corepack; \
    node --version; npm --version; pnpm --version; yarn --version

ENV COREPACK_HOME=/usr/local/lib/corepack \
    COREPACK_ENABLE_DOWNLOAD_PROMPT=0 \
    NPM_CONFIG_UPDATE_NOTIFIER=false

WORKDIR /var/www/html
CMD ["sleep", "infinity"]
