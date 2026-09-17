# Staqio PHP runtime image.
#
# Based on the official php:<version>-fpm image with all extensions Staqio offers
# compiled in but NOT enabled. Staqio enables them per project through the generated
# /usr/local/etc/php/conf.d/zz-staqio.ini (extension=... lines), so toggling an
# extension in the UI never requires a rebuild.
#
# Build: docker build --build-arg PHP_VERSION=8.4 -t ghcr.io/seramos/staqio-php:8.4 images/php
# Pre-release versions use a different upstream tag, e.g. --build-arg BASE_TAG=8.6-rc.
ARG PHP_VERSION=8.4
ARG BASE_TAG=${PHP_VERSION}
FROM php:${BASE_TAG}-fpm

ARG PHP_VERSION
ARG BASE_TAG
LABEL org.opencontainers.image.title="Staqio PHP ${PHP_VERSION}" \
      org.opencontainers.image.source="https://github.com/seramos/staqio" \
      staqio.runtime="php" \
      staqio.runtime.version="${PHP_VERSION}"

# Extensions that can be toggled per project (keep in sync with internal/runtime/catalog.go).
ENV STAQIO_PHP_EXTENSIONS="mysqli pdo_mysql pdo_pgsql gd intl zip bcmath opcache imagick"

ADD --chmod=0755 https://github.com/mlocati/docker-php-extension-installer/releases/latest/download/install-php-extensions /usr/local/bin/

# Extensions are installed one by one so a single incompatible extension is identifiable.
# Stable versions must build every extension; pre-release versions (BASE_TAG *-rc) may skip
# extensions that do not compile yet - they are listed in /etc/staqio/missing-extensions.
RUN set -eux; \
    mkdir -p /etc/staqio; : > /etc/staqio/missing-extensions; \
    for ext in ${STAQIO_PHP_EXTENSIONS}; do \
        if ! install-php-extensions "${ext}"; then \
            echo "${ext}" >> /etc/staqio/missing-extensions; \
            case "${BASE_TAG}" in \
                *-rc*) echo "WARNING: ${ext} not available for PHP ${PHP_VERSION} (preview), skipping";; \
                *) echo "ERROR: ${ext} failed to build for PHP ${PHP_VERSION}"; exit 1;; \
            esac; \
        fi; \
    done; \
    # Compiled in, but disabled by default: Staqio's per-project ini enables what is selected.
    for ext in ${STAQIO_PHP_EXTENSIONS}; do rm -f "/usr/local/etc/php/conf.d/docker-php-ext-${ext}.ini"; done; \
    # Tools used by project actions (Phase 5).
    apt-get update; \
    apt-get install -y --no-install-recommends git unzip openssh-client; \
    rm -rf /var/lib/apt/lists/*; \
    php -m

COPY --from=composer:2 /usr/bin/composer /usr/local/bin/composer

WORKDIR /var/www/html
