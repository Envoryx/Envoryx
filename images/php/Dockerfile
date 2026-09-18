# Envoryx PHP runtime image.
#
# Based on the official php:<version>-fpm image with all extensions Envoryx offers
# compiled in but NOT enabled. Envoryx enables them per project through the generated
# /usr/local/etc/php/conf.d/zz-envoryx.ini (extension=... lines), so toggling an
# extension in the UI never requires a rebuild.
#
# Build: docker build --build-arg PHP_VERSION=8.4 -t ghcr.io/envoryx/envoryx-php:8.4 images/php
# Pre-release versions use a different upstream tag, e.g. --build-arg BASE_TAG=8.6-rc.
ARG PHP_VERSION=8.4
ARG BASE_TAG=${PHP_VERSION}
FROM php:${BASE_TAG}-fpm

ARG PHP_VERSION
ARG BASE_TAG
LABEL org.opencontainers.image.title="Envoryx PHP ${PHP_VERSION}" \
      org.opencontainers.image.source="https://github.com/envoryx/envoryx" \
      envoryx.runtime="php" \
      envoryx.runtime.version="${PHP_VERSION}"

# Extensions that can be toggled per project (keep in sync with internal/runtime/catalog.go).
ENV ENVORYX_PHP_EXTENSIONS="mysqli pdo_mysql pdo_pgsql mongodb gd intl zip bcmath opcache imagick xdebug"

ADD --chmod=0755 https://github.com/mlocati/docker-php-extension-installer/releases/latest/download/install-php-extensions /usr/local/bin/

# Extensions are installed one by one so a single incompatible extension is identifiable.
# Stable versions must build every extension; pre-release versions (BASE_TAG *-rc) may skip
# extensions that do not compile yet - they are listed in /etc/envoryx/missing-extensions.
RUN set -eux; \
    mkdir -p /etc/envoryx; : > /etc/envoryx/missing-extensions; \
    for ext in ${ENVORYX_PHP_EXTENSIONS}; do \
        if ! install-php-extensions "${ext}"; then \
            echo "${ext}" >> /etc/envoryx/missing-extensions; \
            case "${BASE_TAG}" in \
                *-rc*) echo "WARNING: ${ext} not available for PHP ${PHP_VERSION} (preview), skipping";; \
                *) echo "ERROR: ${ext} failed to build for PHP ${PHP_VERSION}"; exit 1;; \
            esac; \
        fi; \
    done; \
    # Compiled in, but disabled by default: Envoryx's per-project ini enables what is selected.
    for ext in ${ENVORYX_PHP_EXTENSIONS}; do rm -f "/usr/local/etc/php/conf.d/docker-php-ext-${ext}.ini"; done; \
    # Tools used by project actions (Phase 5).
    apt-get update; \
    # git/unzip/ssh for actions; socat relays SSH port forwards to the IDE backend on 127.0.0.1;
    # procps + X libs let JetBrains Gateway run its IDE backend here.
    apt-get install -y --no-install-recommends git unzip openssh-client socat procps libfreetype6 fontconfig libxi6 libxrender1 libxtst6 libxext6; \
    rm -rf /var/lib/apt/lists/*; \
    php -m

COPY --from=composer:2 /usr/bin/composer /usr/local/bin/composer

WORKDIR /var/www/html
