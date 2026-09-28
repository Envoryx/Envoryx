# Envoryx .NET runtime image: the .NET SDK (Ubuntu) plus dotnet-ef, the netcoredbg
# debugger, curl, unzip and socat. Runs idle (sleep) as a tooling container; the
# application server is started as the container's main process when the project enables
# it.
#
# Build: docker build --build-arg BASE_TAG=10.0-noble --build-arg DOTNET_VERSION=10 -t ghcr.io/envoryx/envoryx-dotnet:10 images/dotnet
ARG BASE_TAG=10.0-noble
FROM mcr.microsoft.com/dotnet/sdk:${BASE_TAG}

ARG DOTNET_VERSION=10
ARG TARGETARCH
LABEL org.opencontainers.image.title="Envoryx .NET ${DOTNET_VERSION}" \
      org.opencontainers.image.source="https://github.com/envoryx/envoryx" \
      envoryx.runtime="dotnet" \
      envoryx.runtime.version="${DOTNET_VERSION}"

# netcoredbg (MIT) is the debugger IDEs start in the container over SSH: VS Code through
# pipeTransport. Microsoft's vsdbg may only be used from Microsoft's own IDEs. It is pinned
# with its checksums, per architecture; to bump it, change the version and both checksums
# (sha256sum of the two release tarballs).
ARG NETCOREDBG_VERSION=3.2.0-1092
ARG NETCOREDBG_SHA256_AMD64=080eb3b2d2152465f599d3b33d1ee6e747794e11cc0a3773ec689f5e5f2c5afa
ARG NETCOREDBG_SHA256_ARM64=065ff49badec8a695dbea2de6ab6a330c774a191e426a217ab8cc05250627ccb
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        git openssh-client ca-certificates curl unzip socat procps tini; \
    rm -rf /var/lib/apt/lists/*; \
    case "${TARGETARCH:-amd64}" in \
        amd64) sum="${NETCOREDBG_SHA256_AMD64}" ;; \
        arm64) sum="${NETCOREDBG_SHA256_ARM64}" ;; \
        *) echo "no netcoredbg for ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/netcoredbg.tar.gz "https://github.com/Samsung/netcoredbg/releases/download/${NETCOREDBG_VERSION}/netcoredbg-linux-${TARGETARCH:-amd64}.tar.gz"; \
    echo "${sum}  /tmp/netcoredbg.tar.gz" | sha256sum -c -; \
    tar -xzf /tmp/netcoredbg.tar.gz -C /opt; \
    rm -f /tmp/netcoredbg.tar.gz; \
    ln -s /opt/netcoredbg/netcoredbg /usr/local/bin/netcoredbg; \
    netcoredbg --version | head -n 1

# dotnet-ef for the migrations, in the SDK's major version, installed for every user (the
# containers run as the project owner, whose home is the project's).
ENV DOTNET_CLI_TELEMETRY_OPTOUT=1 \
    DOTNET_NOLOGO=1 \
    PATH="/opt/dotnet-tools:${PATH}"
RUN dotnet tool install --tool-path /opt/dotnet-tools dotnet-ef --version "${DOTNET_VERSION}.*" \
    && dotnet ef --version \
    && rm -rf /root/.nuget /root/.dotnet /tmp/NuGetScratch*

# SSH sessions (and IDEs probing over SSH) run a login shell, whose /etc/profile resets
# PATH, and dotnet-ef would vanish.
RUN printf '%s\n' 'export PATH="/opt/dotnet-tools:$PATH"' 'export DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1' > /etc/profile.d/envoryx-dotnet.sh

WORKDIR /var/www/html
# dotnet watch starts the application as a child process; tini as PID 1 reaps what it
# orphans and forwards signals.
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["sleep", "infinity"]
