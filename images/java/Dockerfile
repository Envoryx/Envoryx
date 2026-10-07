# Envoryx Java runtime image: Eclipse Temurin JDK plus Maven and Gradle, git, curl and
# unzip (the Spring Boot and Quarkus templates download their projects) and socat. Runs
# idle (sleep) as a tooling container; the application server is started as the
# container's main process when the project enables it.
#
# Build: docker build --build-arg BASE_TAG=25-jdk-noble -t ghcr.io/envoryx/envoryx-java:25 images/java
ARG BASE_TAG=25-jdk-noble
FROM eclipse-temurin:${BASE_TAG}

ARG JAVA_VERSION
LABEL org.opencontainers.image.title="Envoryx Java ${JAVA_VERSION}" \
      org.opencontainers.image.source="https://github.com/envoryx/envoryx" \
      envoryx.runtime="java" \
      envoryx.runtime.version="${JAVA_VERSION}"

# Maven and Gradle are pinned with their checksums, so a rebuild never ships a build tool
# nobody chose. To bump one, change the version and the checksum together: Maven publishes
# the .sha512 next to the tarball, Gradle the .sha256 next to the zip. A project's own
# wrapper (mvnw, gradlew) wins over these anyway.
ARG MAVEN_VERSION=3.9.16
ARG MAVEN_SHA512=831a8591fe20c8243b1dbe7d71e3244f31d1665b0804b2e825e38cbbe5ce0cafb8338851f90780735568773e0a6cd07bbec107cda0b896b008b861075358b6f6
ARG GRADLE_VERSION=9.8.0
ARG GRADLE_SHA256=bafd5ce9cfaea0fbccfdc8439a1ac42fbd4cd9c89dc9a988228d8a2639a58e6c
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        git openssh-client rsync ca-certificates curl unzip socat procps tini \
        libfreetype6 fontconfig libxi6 libxrender1 libxtst6 libxext6; \
    rm -rf /var/lib/apt/lists/*; \
    curl -fsSL -o /tmp/maven.tar.gz "https://archive.apache.org/dist/maven/maven-3/${MAVEN_VERSION}/binaries/apache-maven-${MAVEN_VERSION}-bin.tar.gz"; \
    echo "${MAVEN_SHA512}  /tmp/maven.tar.gz" | sha512sum -c -; \
    mkdir -p /opt/maven; \
    tar -xzf /tmp/maven.tar.gz -C /opt/maven --strip-components=1; \
    curl -fsSL -o /tmp/gradle.zip "https://services.gradle.org/distributions/gradle-${GRADLE_VERSION}-bin.zip"; \
    echo "${GRADLE_SHA256}  /tmp/gradle.zip" | sha256sum -c -; \
    unzip -q /tmp/gradle.zip -d /opt; \
    mv "/opt/gradle-${GRADLE_VERSION}" /opt/gradle; \
    rm -f /tmp/maven.tar.gz /tmp/gradle.zip; \
    ln -s /opt/maven/bin/mvn /usr/local/bin/mvn; \
    ln -s /opt/gradle/bin/gradle /usr/local/bin/gradle; \
    java -version; mvn -v; gradle --version --no-daemon -q >/dev/null

# Spring Boot's Gradle plugin has no property for the JVM arguments of bootRun, so this
# init script hands it ENVORYX_JDWP (the debug agent) when the start script sets it.
RUN mkdir -p /opt/envoryx && printf '%s\n' \
    'allprojects {' \
    '    tasks.withType(JavaExec).configureEach { task ->' \
    "        if (task.name == 'bootRun' && System.getenv('ENVORYX_JDWP')) {" \
    "            task.jvmArgs(System.getenv('ENVORYX_JDWP'))" \
    '        }' \
    '    }' \
    '}' > /opt/envoryx/jdwp.gradle

# SSH sessions (and IDEs probing over SSH) run a login shell, whose /etc/profile resets
# PATH, and java (which lives under JAVA_HOME, not in /usr/local/bin) would vanish.
RUN printf '%s\n' "export JAVA_HOME=${JAVA_HOME}" 'export PATH="$JAVA_HOME/bin:$PATH"' > /etc/profile.d/envoryx-java.sh

WORKDIR /var/www/html
# Maven and Gradle start the application as a child JVM in dev mode; tini as PID 1 reaps
# what they orphan and forwards signals.
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["sleep", "infinity"]
