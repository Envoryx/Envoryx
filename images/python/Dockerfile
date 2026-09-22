# Envoryx Python runtime image: official python image plus uv, git and the build
# dependencies common wheels need when no binary is available (psycopg, mysqlclient,
# Pillow, cryptography). Runs idle (sleep) as a tooling container; the application server
# is started as the container's main process when the project enables it.
#
# Build: docker build --build-arg BASE_TAG=3.13-slim-bookworm -t ghcr.io/envoryx/envoryx-python:3.13 images/python
ARG BASE_TAG=3.13-slim-bookworm
FROM python:${BASE_TAG}

ARG PYTHON_VERSION
LABEL org.opencontainers.image.title="Envoryx Python ${PYTHON_VERSION}" \
      org.opencontainers.image.source="https://github.com/envoryx/envoryx" \
      envoryx.runtime="python" \
      envoryx.runtime.version="${PYTHON_VERSION}"

# uv is pinned to a release series (like the base image tag) so a rebuild of an
# unchanged version does not silently ship a different uv; bump it deliberately.
COPY --from=ghcr.io/astral-sh/uv:0.12 /uv /uvx /usr/local/bin/

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        git openssh-client ca-certificates socat procps \
        build-essential pkg-config \
        libpq-dev default-libmysqlclient-dev \
        libjpeg62-turbo-dev zlib1g-dev libffi-dev libssl-dev \
        libfreetype6 fontconfig libxi6 libxrender1 libxtst6 libxext6; \
    rm -rf /var/lib/apt/lists/*; \
    python --version; pip --version; uv --version

ENV PYTHONUNBUFFERED=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    UV_LINK_MODE=copy

WORKDIR /var/www/html
CMD ["sleep", "infinity"]
