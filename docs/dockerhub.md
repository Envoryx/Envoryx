<p align="center">
  <img src="https://raw.githubusercontent.com/envoryx/envoryx/main/docs/assets/envoryx-logo.png" alt="Envoryx - build deeper" width="480">
</p>

**Development environments on your own server, set up from a web UI.** For Linux Docker
hosts and Unraid, x86_64 and arm64.

Envoryx is a single container that gives each of your projects its own little stack: a web
server, the runtime it needs (PHP, Node.js, Python, Go, Ruby, Java or .NET), a database and
whatever services you add. You click a project together instead of writing
`docker-compose.yml` files, and it answers at `https://<project>.test` with a trusted
certificate. Your editor connects over SSH, backups run on a schedule, and the whole thing
lives on a home server, a NAS or a cheap VM instead of your laptop.

![The Envoryx dashboard](https://raw.githubusercontent.com/envoryx/envoryx/main/docs/assets/screenshots/dashboard-light.png)

## Quick start

```sh
mkdir -p ~/envoryx/config ~/envoryx/projects
docker run -d --name envoryx --restart unless-stopped --stop-timeout 90 \
  -p 8787:8787 -p 80:80 -p 443:443 -p 2222:2222 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v ~/envoryx/config:/config -v ~/envoryx/projects:/projects \
  -e PUID=$(id -u) -e PGID=$(id -g) -e ENVORYX_SHUTDOWN_GRACE=80s \
  envoryx/envoryx:latest
```

Open `http://<your-server>:8787`, create the admin account and click **New project**. Ports
80 and 443 are for reaching projects by name, 2222 for your IDE; leave them out if you don't
need that yet. The [installation guide](https://envoryx.dev/docs/getting-started/installation/)
has the same as a Compose file, and there's an
[Unraid template](https://envoryx.dev/docs/getting-started/unraid/).

## Tags

| Tag | What |
|---|---|
| `latest` | the newest release |
| `<version>`, e.g. `0.46.5` | a fixed release |
| `<major>.<minor>`, e.g. `0.46` | the newest release of that line |

The images here are copies of `ghcr.io/envoryx/envoryx` with the same digests. Each one
has a build attestation signed by GitHub, an SBOM and images for `linux/amd64` and
`linux/arm64`. An instance updates itself from the registry it was started from.

## Links

- Documentation: [envoryx.dev/docs](https://envoryx.dev/docs/)
- Source, issues and changelog: [github.com/envoryx/envoryx](https://github.com/envoryx/envoryx)
- License: [AGPL-3.0-only](https://github.com/envoryx/envoryx/blob/main/LICENSE)
