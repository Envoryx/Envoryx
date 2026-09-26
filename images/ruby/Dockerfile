# Envoryx Ruby runtime image: official ruby image plus the debug gem (rdbg, which
# RubyMine and VS Code attach to), git and the build dependencies common gems need
# (pg, mysql2, sqlite3, psych). No Node.js: JavaScript bundling runs in the project's
# Node service. Runs idle (sleep) as a tooling container; the application server is
# started as the container's main process when the project enables it.
#
# Build: docker build --build-arg BASE_TAG=4.0-slim-bookworm -t ghcr.io/envoryx/envoryx-ruby:4.0 images/ruby
ARG BASE_TAG=4.0-slim-bookworm
FROM ruby:${BASE_TAG}

ARG RUBY_VERSION_LABEL
LABEL org.opencontainers.image.title="Envoryx Ruby ${RUBY_VERSION_LABEL}" \
      org.opencontainers.image.source="https://github.com/envoryx/envoryx" \
      envoryx.runtime="ruby" \
      envoryx.runtime.version="${RUBY_VERSION_LABEL}"

# The debug gem is pinned to a release series like the base image tag, so a rebuild of
# an unchanged version does not silently ship a different rdbg; bump it deliberately. It
# goes to the image's GEM_HOME (/usr/local/bundle), which the containers keep on GEM_PATH.
ARG DEBUG_GEM_VERSION="~> 1.11"
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        git openssh-client ca-certificates curl socat procps tini tzdata \
        build-essential pkg-config \
        libyaml-dev libffi-dev libssl-dev zlib1g-dev \
        libpq-dev default-libmysqlclient-dev libsqlite3-dev sqlite3 \
        libfreetype6 fontconfig libxi6 libxrender1 libxtst6 libxext6; \
    rm -rf /var/lib/apt/lists/*; \
    gem install --no-document debug -v "${DEBUG_GEM_VERSION}"; \
    ruby --version; gem --version; bundle --version; rdbg --version

# SSH sessions (and IDEs probing over SSH) run a login shell, whose /etc/profile resets
# PATH – the executables of the gems in the project home (GEM_HOME, set by Envoryx) and
# rdbg would vanish.
RUN printf '%s\n' 'export PATH="${GEM_HOME:-/usr/local/bundle}/bin:/usr/local/bundle/bin:$PATH"' > /etc/profile.d/envoryx-ruby.sh

WORKDIR /var/www/html
# rdbg and Puma start child processes; tini as PID 1 reaps what they orphan and forwards
# signals.
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["sleep", "infinity"]
