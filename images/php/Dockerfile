# Envoryx PHP runtime image.
#
# Based on the official php:<version>-fpm image with all extensions Envoryx offers
# compiled in but NOT enabled. Envoryx enables them per project through the generated
# /usr/local/etc/php/conf.d/zz-envoryx.ini (extension=... lines), so toggling an
# extension in the UI never requires a rebuild.
#
# Build: docker build --build-arg PHP_VERSION=8.4 -t ghcr.io/envoryx/envoryx-php:8.4 images/php
# Pre-release versions use a different upstream tag and may lack extensions, e.g.
# --build-arg BASE_TAG=8.6-rc --build-arg MISSING_EXTENSIONS="mongodb imagick ..." (see below).
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
ENV ENVORYX_PHP_EXTENSIONS="mysqli pdo_mysql pdo_pgsql mongodb gd intl zip bcmath opcache imagick redis memcached amqp xdebug"

ADD --chmod=0755 https://github.com/mlocati/docker-php-extension-installer/releases/latest/download/install-php-extensions /usr/local/bin/

# The extensions this version's image may lack, from "missingExtensions" in
# internal/runtime/php_versions.json (php-images.yml passes them). Envoryx refuses to switch
# those on, so any other extension that fails to build fails the image.
ARG MISSING_EXTENSIONS=""

# Extensions are installed one by one so a single incompatible extension is identifiable.
# Skipped ones are listed in /etc/envoryx/missing-extensions.
RUN set -eux; \
    mkdir -p /etc/envoryx; : > /etc/envoryx/missing-extensions; \
    # Always on (BuiltIn in internal/runtime/catalog.go), but not every upstream image compiles
    # them in: php:8.6-rc dropped mbstring, and none has exif (WordPress, Drupal and TYPO3 read
    # photo orientation with it). First, as pickle (PECL builds) needs mbstring;
    # libonig-dev by hand, as the installer knows no build dependencies for unreleased versions.
    if ! php -m | grep -qix mbstring; then apt-get update; apt-get install -y --no-install-recommends libonig-dev; fi; \
    for ext in mbstring curl pdo_sqlite exif; do php -m | grep -qix "${ext}" || install-php-extensions "${ext}"; done; \
    for ext in ${ENVORYX_PHP_EXTENSIONS}; do \
        if ! install-php-extensions "${ext}"; then \
            echo "${ext}" >> /etc/envoryx/missing-extensions; \
            case " ${MISSING_EXTENSIONS} " in \
                *" ${ext} "*) echo "WARNING: ${ext} not available for PHP ${PHP_VERSION}, skipping (listed in php_versions.json)";; \
                *) echo "ERROR: ${ext} failed to build for PHP ${PHP_VERSION}; fix it or add it to missingExtensions in php_versions.json"; exit 1;; \
            esac; \
        elif case " ${MISSING_EXTENSIONS} " in *" ${ext} "*) true;; *) false;; esac; then \
            echo "NOTE: ${ext} builds for PHP ${PHP_VERSION} now; drop it from missingExtensions in php_versions.json"; \
        fi; \
    done; \
    # Compiled in, but disabled by default: Envoryx's per-project ini enables what is selected.
    # The installer names some inis xx-php-ext-* (memcached) to load them after their dependencies.
    for ext in ${ENVORYX_PHP_EXTENSIONS}; do rm -f "/usr/local/etc/php/conf.d/docker-php-ext-${ext}.ini" "/usr/local/etc/php/conf.d/xx-php-ext-${ext}.ini"; done; \
    # Tools used by project actions.
    apt-get update; \
    # git/unzip/ssh for actions; socat relays SSH port forwards to the IDE backend on 127.0.0.1;
    # procps + X libs let JetBrains Gateway run its IDE backend here.
    # msmtp delivers mail() to Mailpit; Envoryx sets sendmail_path only while Mailpit is on.
    # No msmtp-mta: without Mailpit /usr/sbin/sendmail stays missing and mail() returns false.
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends git unzip openssh-client socat procps libfreetype6 fontconfig libxi6 libxrender1 libxtst6 libxext6 msmtp; \
    rm -rf /var/lib/apt/lists/*; \
    php -m

# Tools CMSs and frameworks expect on the server.
# - imagemagick: TYPO3 resizes and crops through the convert/magick binaries, not the imagick
#   extension.
# - Locales: TYPO3 site configs set e.g. locale: en_US.UTF-8, and setlocale() fails for anything
#   the image does not have. A few common ones, all would be over 200 MB.
# - Database clients for drush sql:*, artisan db, wp db and deploy scripts. pg_dump refuses
#   servers newer than itself, so psql comes from the PostgreSQL apt repository (newest major);
#   images whose Debian that repository no longer serves get the distribution's client.
# - rsync for rsync over SSH.
RUN set -eux; \
    apt-get update; \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends imagemagick locales default-mysql-client rsync; \
    printf '%s UTF-8\n' en_US.UTF-8 en_GB.UTF-8 de_DE.UTF-8 de_AT.UTF-8 de_CH.UTF-8 fr_FR.UTF-8 es_ES.UTF-8 it_IT.UTF-8 nl_NL.UTF-8 pt_BR.UTF-8 pl_PL.UTF-8 > /etc/locale.gen; \
    locale-gen; \
    . /etc/os-release; \
    curl -fsSL -o /usr/share/keyrings/pgdg.asc https://www.postgresql.org/media/keys/ACCC4CF8.asc; \
    echo "deb [signed-by=/usr/share/keyrings/pgdg.asc] https://apt.postgresql.org/pub/repos/apt ${VERSION_CODENAME}-pgdg main" > /etc/apt/sources.list.d/pgdg.list; \
    if apt-get update && pg=$(apt-cache search --names-only '^postgresql-client-[0-9]+$' | sed 's/ .*//' | sort -t- -k3 -n | tail -n1) && [ -n "${pg}" ]; then \
        apt-get install -y --no-install-recommends "${pg}"; \
    else \
        rm /etc/apt/sources.list.d/pgdg.list; apt-get update; apt-get install -y --no-install-recommends postgresql-client; \
    fi; \
    # The MariaDB client checks server certificates by default, which fails against MySQL's and
    # MariaDB's self-signed ones. The database sits on the project's own network.
    mkdir -p /etc/mysql/conf.d; printf '[client]\nssl-verify-server-cert = off\n' > /etc/mysql/conf.d/zz-envoryx.cnf; \
    rm -rf /var/lib/apt/lists/*; \
    convert -version; psql --version; mysql --version; mysqldump --version; rsync --version | head -n1; php -m | grep -ix exif

COPY --from=composer:2 /usr/bin/composer /usr/local/bin/composer
# WP-CLI as wp; the weekly image build picks up new releases.
ADD --chmod=0755 https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar /usr/local/bin/wp

WORKDIR /var/www/html
