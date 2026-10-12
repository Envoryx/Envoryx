# Sources of the images' third-party software

The Envoryx image (`ghcr.io/envoryx/envoryx`) and the runtime images
(`ghcr.io/envoryx/envoryx-<runtime>`) contain software that others wrote, under their own
licences. Some of it is under the GNU GPL, which asks whoever passes the binaries on to say
where the matching source code is. This file is that place; each image points here with the
label `org.opencontainers.image.documentation`.

Envoryx's own code is at https://github.com/envoryx/envoryx under the AGPL-3.0. The licences
of the Go modules and npm packages built into the envoryx program are in the image at
`/usr/share/doc/envoryx/third-party-licenses.txt` and in the web interface (the logo's page,
*Third-party licenses*).

## Base images and their packages

Every image starts from an official image and adds operating system packages with the
distribution's package manager. Their sources are the distribution's:

| Image | Base | Distribution sources |
|---|---|---|
| envoryx | `alpine` | https://gitlab.alpinelinux.org/alpine/aports (the APKBUILDs name each package's upstream source) |
| envoryx-php | `php:<version>-fpm` (Debian) | https://sources.debian.org, or `apt-get source <package>`; past versions at https://snapshot.debian.org |
| envoryx-node | `node:<version>-bookworm-slim` | as above (Debian) |
| envoryx-python | `python:<version>-slim-bookworm` | as above (Debian) |
| envoryx-ruby | `ruby:<version>-slim-bookworm` | as above (Debian) |
| envoryx-go | `golang:<version>-bookworm` | as above (Debian) |
| envoryx-java | `eclipse-temurin:<version>-jdk-noble` (Ubuntu) | https://launchpad.net/ubuntu (each package's *Download* section), or `apt-get source <package>` |
| envoryx-dotnet | `mcr.microsoft.com/dotnet/sdk:<version>-noble` (Ubuntu) | as above (Ubuntu) |

`dpkg -l` (Debian, Ubuntu) or `apk info -v` (Alpine) in a container lists the exact package
versions an image contains. The official base images publish their own build files:
https://github.com/docker-library (php, node, python, ruby, golang),
https://github.com/adoptium/containers (Temurin) and https://github.com/dotnet/dotnet-docker.

## Software Envoryx adds

| Image | Software | Licence | Source |
|---|---|---|---|
| envoryx-go | Air (live reload) | GPL-3.0 | https://github.com/air-verse/air at the tag that `go version -m /usr/local/bin/air` prints |
| envoryx-go | Delve, gotestsum | MIT, Apache-2.0 | https://github.com/go-delve/delve, https://github.com/gotestyourself/gotestsum |
| envoryx-java | Eclipse Temurin JDK | GPL-2.0 with the Classpath Exception | https://github.com/adoptium/jdk (the version `java -version` prints) |
| envoryx-java | Maven, Gradle | Apache-2.0 | https://archive.apache.org/dist/maven/, https://github.com/gradle/gradle |
| envoryx-php | PHP extensions (xdebug, imagick, redis, memcached, amqp, mongodb …) | PHP-3.01, Xdebug-1.03, Apache-2.0 | https://pecl.php.net, built by https://github.com/mlocati/docker-php-extension-installer |
| envoryx-php | Composer, WP-CLI, Node.js | MIT | https://github.com/composer/composer, https://github.com/wp-cli/wp-cli, https://github.com/nodejs/node |
| envoryx-python | uv | Apache-2.0 or MIT | https://github.com/astral-sh/uv |
| envoryx-ruby | debug gem (rdbg) | BSD-2-Clause | https://github.com/ruby/debug |
| envoryx-dotnet | netcoredbg, dotnet-ef | MIT | https://github.com/Samsung/netcoredbg, https://github.com/dotnet/efcore |

If a source link above no longer leads to the version an image contains, ask in an issue at
https://github.com/envoryx/envoryx/issues and we'll provide it.

Not part of any image: Microsoft's vsdbg and the JetBrains IDE backends. Envoryx downloads
them on request from Microsoft and JetBrains, under those companies' own terms.
