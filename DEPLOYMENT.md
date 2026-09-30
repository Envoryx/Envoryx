# Deployment

This is the reference for running Envoryx: installing it, configuring it and
what each part of it does once your projects are running.

## Requirements

- Linux host with Docker Engine ≥ 24 (API ≥ 1.43); Unraid 6.12+ / 7.x or
  any other Linux distribution
- x86_64 or arm64 (Raspberry Pi 4/5, Ampere/Graviton, Apple Silicon under
  Linux); all Envoryx images are multi-arch
- A directory for Envoryx's state (`/config`) and one for your projects (`/projects`);
  optionally a third one for backups (`/backups`, see [Backups](#backups))

## Docker Compose (any Linux host)

Unraid is the primary target but nothing depends on it. On another Linux host
pick two directories and your own uid/gid:

```yaml
services:
  envoryx:
    image: ghcr.io/envoryx/envoryx:latest
    container_name: envoryx
    ports:
      - "8787:8787"
      - "80:80"     # proxy: projects by domain (optional, any free host port)
      - "443:443"   # proxy: HTTPS via local CA (optional)
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /mnt/user/appdata/envoryx:/config
      - /mnt/user/development:/projects
    environment:
      PUID: 99      # Unraid: nobody; elsewhere: your uid (`id -u`)
      PGID: 100     # Unraid: users;  elsewhere: your gid (`id -g`)
      ENVORYX_PORT_RANGE_START: 20000
      ENVORYX_PORT_RANGE_END: 20999
    restart: unless-stopped
```

`deploy/docker-compose.yml` contains a commented version of this file.
Replace `/mnt/user/...` with e.g. `/srv/envoryx` and `/home/you/dev` on a
regular server.

Start with `docker compose up -d`, open `http://<host>:8787` and create the
admin account. Project web servers are published on ports from the configured
range, e.g. `http://<host>:20000`, and, once the proxy ports are mapped and DNS
is set up (see [Domains and HTTPS](#domains-and-https)), as
`https://<project>.test`.

### Building locally

```
docker build -t ghcr.io/envoryx/envoryx:dev --build-arg VERSION=dev .
```

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ENVORYX_LISTEN` | `:8787` | Listen address |
| `ENVORYX_CONFIG_DIR` | `/config` | Persistent state (SQLite, generated configs, caches). Must be on a local filesystem, see [Where to put /config](#where-to-put-config) |
| `ENVORYX_ALLOW_NETWORK_FS` | `false` | Start even if `/config` is on NFS/SMB. Not recommended: SQLite corrupts silently without reliable file locks |
| `ENVORYX_PROJECTS_DIR` | `/projects` | Root of all project directories |
| `ENVORYX_BACKUPS_DIR` | `/backups` if that directory exists, else `/config/backups` | Where project backups are stored (see [Backups](#backups)) |
| `ENVORYX_PROJECTS_HOST_PATH` | auto | Host path behind `/projects` (see below) |
| `ENVORYX_CONFIG_HOST_PATH` | auto | Host path behind `/config` |
| `DOCKER_HOST` | unix socket | Docker endpoint; set to a socket proxy URL if used |
| `PUID` / `PGID` | `99` / `100` | uid/gid project containers run as and project dirs are owned by |
| `ENVORYX_PORT_RANGE_START` / `_END` | `20000` / `20999` | Host ports assigned to project web servers |
| `ENVORYX_NETWORK_POOL` | `10.213.0.0/16` | IPv4 range each new project network gets a /24 of (256 projects per /16); `off` leaves the choice to Docker, whose default ranges run out after about 30 networks. Pick a range your LAN and VPN don't use |
| `ENVORYX_PUBLIC_HOST` | browser address | Host/IP used for project links (see below); also editable in Settings |
| `ENVORYX_PROXY_HTTP` | `:80` | Listen address of the embedded proxy inside the container; empty disables it |
| `ENVORYX_PROXY_HTTPS` | `:443` | HTTPS listener of the proxy (local CA); empty disables HTTPS |
| `ENVORYX_SSH` | `:2222` | Embedded SSH server for IDE remote interpreters; empty disables it |
| `ENVORYX_ADMIN_USER` / `ENVORYX_ADMIN_PASSWORD` | - | Create the first admin non-interactively |
| `ENVORYX_SECRET_KEY` | - | Key that encrypts the stored secrets (base64 of 32 bytes or 64 hex digits, e.g. `openssl rand -base64 32`); without it Envoryx keeps one in `/config/secret.key`, see [Secret key](#secret-key) |
| `ENVORYX_SECRET_KEY_OLD` | - | The previous key, for one start after changing `ENVORYX_SECRET_KEY` |
| `ENVORYX_SESSION_IDLE_TIMEOUT` | `12h` | Sliding session expiry |
| `ENVORYX_SESSION_ABSOLUTE_TIMEOUT` | `168h` | Hard session expiry |
| `ENVORYX_SECURE_COOKIES` | `false` | Mark cookies `Secure` (enable behind HTTPS) |
| `ENVORYX_UPDATE_CHECK` | `true` | Ask GitHub once a day for a newer release and show a hint in the UI (one anonymous request; `false` disables it) |
| `ENVORYX_SHUTDOWN_GRACE` | `8s` | How long running project operations may finish after a stop signal, see [Stopping and restarting](#stopping-and-restarting) |
| `ENVORYX_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `ENVORYX_LOG_FORMAT` | `json` | `json` or `text` |

## Where to put /config

`/config` holds the SQLite database. SQLite needs a filesystem with working
`fsync` and POSIX locks, so at startup Envoryx checks what `/config` is on:

- **Network filesystem (NFS, SMB/CIFS, 9p, Ceph)**: Envoryx refuses to start.
  Locks are unreliable there and the database corrupts silently; the classic
  "SQLite is unreliable" story is almost always this. Use a local directory;
  `ENVORYX_ALLOW_NETWORK_FS=true` overrides the check at your own risk.
- **FUSE** (Unraid's `/mnt/user/...` user shares): Envoryx starts and shows a
  warning in *Settings*. It works in practice, but the pool path is the safer
  choice for a database: use `/mnt/cache/appdata/envoryx` (or your pool's
  name) as the host path for `/config`, or enable *Exclusive access* for the
  `appdata` share (Unraid ≥ 6.12, share on a single pool). Then `/mnt/user`
  bypasses FUSE and the warning disappears.
- Free space on `/config`, `/projects` and `/backups` is shown on the
  dashboard and checked every five minutes; below 2 GiB or 5 % a
  `storage.low` notification goes out (once, and again when it recovers). A
  project or instance backup is refused when it would not leave at least
  512 MiB free, because a full appdata disk takes the database down with it.
- `/config/logs` holds the log history (see [Logs](#logs)): 7 days and at
  most 1 GB by default, adjustable in *Settings → Retention → Log history*.
- The database file is integrity-checked at every start (`PRAGMA
  integrity_check`). A damaged file is refused with the name of the newest
  instance backup to restore instead of being migrated or served.

## Host paths (important)

Project containers mount your project files with Docker **bind mounts**. Bind
mount sources are resolved by the Docker daemon **on the host**, not inside the
Envoryx container. Envoryx therefore needs to know that `/projects` inside its
container is `/mnt/user/development` on the host.

Envoryx detects this automatically by inspecting its own container's mounts. If
detection fails (the dashboard's notices say so and project creation is
disabled), set the two variables explicitly:

```
ENVORYX_PROJECTS_HOST_PATH=/mnt/user/development
ENVORYX_CONFIG_HOST_PATH=/mnt/user/appdata/envoryx
```

On Unraid always use `/mnt/user/...` (or `/mnt/cache/...`) paths, the same
ones you used in the volume mappings.

## Diagnostics

**Settings → Diagnostics** runs a set-up check and lists every finding with
what it looked at, what it found and how to fix it: Docker engine, where
`/config` lives, host paths, disk space, backup directory, database integrity,
host for project links, proxy ports, wildcard DNS, SSH, HTTPS, version,
project/Docker consistency and notifications. Wildcard DNS is checked twice:
from your browser (the check that matters: it fetches
`envoryx-diagnostics-probe.<base domain>` through the proxy, which also proves
the CA is trusted over HTTPS) and from inside the Envoryx container, whose DNS
server is often a different one (the router instead of your ad blocker); that
second result is only a note.
Findings with a button are fixed in place (for example setting the Docker host
for project links); the others link to the setting or to this guide. The
dashboard lists them among its notices while warnings or errors exist. The same data is
available as `GET /api/v1/system/diagnostics` (admin scope).

## Project links and the Envoryx container's own IP

Project web servers (and Mailpit, database ports, the object storage API and
console) publish their ports on the **Docker host** (the Unraid IP). Envoryx
builds links to them from the address in your browser's address bar. If you
reach Envoryx under a different address, because the container has its own IP
on `br0`/macvlan or you use a reverse proxy, set the host to use for project
links in **Settings → Domains & HTTPS → Project links & developer machine** (or `ENVORYX_PUBLIC_HOST`), typically
the Unraid IP.

When Envoryx detects that it has an IP of its own and no host is set, the
dashboard, the project sections with such links and the settings show a notice
with the Docker host as Docker reports it (name, and its IP when the LAN
resolves the name) and a one-click button to use it.

## Unraid

### Option A: Template (recommended, one click)

Copy the template to your flash drive so it appears under
**Docker → Add Container → Select a template → User templates**:

```
wget -O /boot/config/plugins/dockerMan/templates-user/my-Envoryx.xml \
  https://raw.githubusercontent.com/envoryx/envoryx/main/deploy/unraid/envoryx.xml
```

Then *Add Container → Envoryx → Apply*. Ports, paths, socket, PUID/PGID are
pre-filled; create the `development` share first if it does not exist.

The file name matters. Unraid writes your container settings (network type
such as `br0`, the backups share, ports) to `my-<ContainerName>.xml` on
*Apply*, but *Edit* and *Update* open the **first** file in `templates-user`
(alphabetically) whose `<Name>` matches the container. A second copy of the
template under another name (say `envoryx.xml`) sorts before `my-Envoryx.xml`
and wins, so every edit and every update starts from the repository defaults
and your settings look "reset". Keep exactly one file with `<Name>Envoryx</Name>`
in that directory:

```
ls /boot/config/plugins/dockerMan/templates-user/ | grep -i envoryx
```

If it lists anything besides `my-Envoryx.xml`, delete the extra file, open
the container's edit page once, check the values and *Apply*.

Run the `wget` **once**. Downloading it again overwrites your settings with
the defaults. Updates need no new template: *Docker → Check for Updates →
Apply* pulls the new image and keeps your settings. If a release changes the
template (new variable or path), add the change on the container's edit page
by hand.

Alternatively add `https://github.com/envoryx/envoryx` under
**Docker → Add Container → Template repositories**. Unraid then reads
`deploy/unraid/envoryx.xml` directly from the repository and picks up updates.

No publication in Community Applications is required for either way.

### Option B: Manual

**Docker → Add Container**:

| Setting | Value |
|---------|-------|
| Repository | `ghcr.io/envoryx/envoryx:latest` |
| Network type | bridge |
| Port | `8787` → `8787` |
| Port | `80` → `80` and `443` → `443` (proxy; optional, other host ports work) |
| Port | `2222` → `2222` (SSH for IDEs; optional) |
| Path `/config` | `/mnt/cache/appdata/envoryx` (pool path; `/mnt/user/appdata/envoryx` works with a warning, see [Where to put /config](#where-to-put-config)) |
| Path `/projects` | `/mnt/user/development` (create the share first) |
| Path `/backups` | optional, e.g. `/mnt/user/backups/envoryx` - keeps backups off the cache/appdata share |
| Path `/var/run/docker.sock` | `/var/run/docker.sock` |
| Variable `PUID` | `99` |
| Variable `PGID` | `100` |

Project ports (20000-20999 by default) are published by the project containers
themselves, not by the Envoryx container, so there's nothing else to map in the template.
Unraid shows Envoryx's containers (`envoryx-<project>-…`) in the Docker tab; you
can leave them alone, Envoryx manages them.

### Sorting the containers into a folder (FolderView3)

Every project brings several containers, so the Docker tab fills up quickly. They
all carry the Envoryx icon (`net.unraid.docker.icon`), and the
[FolderView3](https://github.com/kennymc-c/folder.view3) plugin can collect them in a
folder in either of two ways:

- **By name, nothing to set in Envoryx:** create a folder in FolderView3 and enter
  `^envoryx-` as its regex. Every Envoryx container matches, the database browser
  included.
- **By label:** enter the folder name under *Settings → General → Unraid Docker page*
  in Envoryx and create a folder with exactly that name in FolderView3. Envoryx then
  puts `folder.view3=<name>` on its containers. A label wins over any regex, so the
  containers stay put even next to a catch-all folder (regex `^`).

Labels are fixed when a container is created. After the folder name changes, a
project moves the next time it is started (Envoryx recreates its containers for
that; files, databases and volumes are untouched). One folder per project is left
out on purpose: FolderView3 does not create folders, so each would have to be
made by hand.

### Image visibility

The image is built by GitHub Actions (`.github/workflows/docker.yml`) and
pushed to `ghcr.io/envoryx/envoryx`. GitHub creates new packages as **private**;
set the package to *public* once (GitHub → Packages → envoryx → Package settings
→ Change visibility) so Unraid can pull it without credentials. For a private
package run `docker login ghcr.io` on the Unraid server instead (add it to
`/boot/config/go` to survive reboots).

### File ownership

Files created by PHP inside a project are owned by `PUID:PGID` (Unraid default
`nobody:users` = `99:100`), so they are editable via SMB shares. Directories
created by Envoryx itself are chowned to the same ids.

## Domains and HTTPS

Envoryx contains a reverse proxy that routes requests by host name to the
project's web server and serves the Envoryx UI itself. It listens on ports 80
and 443 **inside** the container; map them to host ports (80/443 or any free
ones; Envoryx reads its own port bindings and adjusts links accordingly).
When neither port is published, Settings → *Domains & HTTPS* shows a warning
and project links keep using the direct port.

**Own IP (Unraid `br0`, macvlan/ipvlan) or host networking:** there is no
port mapping; the proxy is reachable directly on the container's address
(`http(s)://<envoryx-ip>`). Envoryx detects this and shows the address in the
settings. Your DNS entries for `*.test` must then point at the **Envoryx IP**,
not at the Unraid IP (which is where the direct project ports live).

### Names

- Base domain, default `test` (Settings → Domains & HTTPS). Every project is
  `<slug>.<base>` (`shop.test`), the UI is `envoryx.<base>`.
- Additional names per project in its **Domains** section (`shop.local`,
  `api.shop.test`, …). Each name must be unique across projects.
- The names must resolve to the Envoryx host on every client. Because Envoryx
  runs on a server, one **wildcard entry** in the DNS server of your network
  does it for every project, present and future. *Settings → Domains & HTTPS →
  DNS for \*.test* shows the entry for each of the servers below with the
  address already filled in. `<server-ip>` is the Docker host, or Envoryx's
  own IP when it has one (see above).
  - **AdGuard Home**: *Filters → DNS rewrites → Add DNS rewrite*, domain
    `*.test`, answer `<server-ip>`.
  - **Pi-hole**: *Local DNS records* cannot hold a wildcard, so the entry goes
    into the dnsmasq Pi-hole is built on: `address=/test/<server-ip>`.
    Pi-hole 6: *Settings → All settings* (Expert mode) *→ Miscellaneous →
    misc.dnsmasq_lines*, then *Save & Apply* (Pi-hole 6 ignores
    `/etc/dnsmasq.d/` unless `misc.etc_dnsmasq_d` is on). Pi-hole 5: put the
    line into `/etc/dnsmasq.d/99-envoryx.conf` and run `pihole restartdns`.
  - **dnsmasq** (server, OpenWrt): `address=/test/<server-ip>` covers `test`
    and every name below it. OpenWrt:
    `uci add_list dhcp.@dnsmasq[0].address='/test/<server-ip>' && uci commit dhcp && /etc/init.d/dnsmasq restart`.
  - **Unbound** (pfSense: *Services → DNS Resolver → Custom options*;
    OPNsense: a `.conf` file in `/usr/local/etc/unbound.opnsense.d/`):
    ```
    server:
      local-zone: "test." redirect
      local-data: "test. IN A <server-ip>"
    ```
  - **hosts file** per client, for routers that know no wildcard (a FritzBox
    has neither wildcard records nor per-domain forwarding):
    `/etc/hosts`, `C:\Windows\System32\drivers\etc\hosts`, one line such as
    `<server-ip> envoryx.test shop.test`. Every new project needs adding by hand.
    The settings page lists all current names.
- **Inside project containers** none of this is needed. Envoryx joins every
  project network and carries all the proxy's host names there as network
  aliases (every project, `-dev`, `-s3`, extra domains, `envoryx.<base>`).
  Docker's own DNS answers them with Envoryx's address on that network. An
  application can therefore call its own URL (`http://shop.test`) or another
  project's, whatever DNS server the Docker host uses and even when Envoryx has
  its own IP on `br0`, which containers on a bridge network cannot reach.
  Docker fixes aliases when a container joins a network, so names added
  meanwhile reach a project the next time it starts. HTTPS from inside a
  container needs the Envoryx CA in that container, or `curl -k`. On bare
  metal there are no aliases, and containers use the Docker host's DNS.
- `.test` is reserved for exactly this purpose (RFC 6761) and never resolves
  on the public internet. `.local` collides with mDNS on macOS/Linux, so prefer
  `.test` or an owned domain (`dev.example.com`).

### HTTPS

On first start Envoryx creates a local certificate authority under
`/config/ca/` (`ca.key` 0600, `ca.crt`). The proxy issues a certificate per
host name on demand, so `https://shop.test` works as soon as the CA is
trusted on the client:

1. Settings → Domains & HTTPS → **Download envoryx-ca.crt**.
2. Install it as a trusted root (instructions per OS are shown next to the
   button; Firefox has its own store).

**Force HTTPS** redirects `http://` requests for known names to HTTPS.

### Let's Encrypt instead of the local CA (no installation on clients)

If you own a domain, Envoryx can obtain and renew a **public wildcard
certificate** itself, so every browser trusts project URLs without any
CA installation. It uses the ACME dns-01 challenge: Envoryx creates a
temporary `_acme-challenge` TXT record through your DNS provider's API,
Let's Encrypt verifies it, done. Nothing needs to be reachable from the
internet and the domain never has to point at your server publicly.

1. Get API credentials from your DNS provider:

   | Provider | Credentials | Where |
   |----------|-------------|-------|
   | Cloudflare | API token | *My Profile → API Tokens → Create Token → template "Edit zone DNS"*, restricted to the zone (*Zone:Read*, *Zone:DNS:Edit*) |
   | Hetzner | API token | Hetzner Console → the project that holds the zone → *Security → API tokens*, *Read & Write*. DNS moved into the Console in 2025; tokens of the old DNS Console (dns.hetzner.com, shut down May 2026) do not work |
   | netcup | customer number, API key, API password | Customer Control Panel → *Master Data → API* |
   | Amazon Route 53 | access key ID, secret access key, optionally the hosted zone ID | an IAM user with `route53:ListHostedZonesByName`, `route53:GetHostedZone`, `route53:ListResourceRecordSets` and `route53:ChangeResourceRecordSets` (the zone ID is only needed when the domain has several public hosted zones) |
   | DigitalOcean | API token | *API → Tokens → Generate New Token* with the domain scopes (read, create, delete) |
   | Porkbun | API key, secret API key | *Account → API Access*; API access must also be switched on for the domain |

2. Settings → Domains & HTTPS → **Let's Encrypt**: provider, domain
   (e.g. `dev.example.com`; the wildcard `*.dev.example.com` is added),
   contact e-mail, the provider's credentials, "Use as base domain" → Enable.
   The zone is found by itself (`dev.example.com` inside `example.com` works);
   secrets are never shown again and stay when their fields are left empty.
3. In your **local** DNS (AdGuard/Pi-hole/…) rewrite `*.dev.example.com` →
   Envoryx's address. Do not create a public record for it.

Envoryx requests the certificate in the background (1-2 minutes, status is
shown in the card; netcup publishes records only after several minutes, so
there Envoryx waits up to 20 minutes, Porkbun up to 10). Before the CA looks,
Envoryx checks that every name server of the zone serves the record. It asks
them directly, so a local resolver or a cached "does not exist" cannot fool the
check. The certificate is stored under `/config/ca/custom.*` and renewed 30
days before expiry. The local CA stays as fallback for other names (`.test`).
The staging checkbox uses Let's Encrypt's staging environment to test the
setup without rate limits (certificates from staging are not trusted).

Let's Encrypt publishes every issued certificate in public
certificate-transparency logs, so the existence of `*.dev.example.com` is
visible there, but nothing else.

**Own certificate**: alternatively upload any wildcard certificate with its
key in the same card. It is used for every name it covers; other names keep
using the local CA. Certificate and key are stored under `/config/ca/custom.*`
with owner-only permissions and are never returned by the API.

The proxy keeps the original `Host`, sets `X-Forwarded-For/-Proto/-Host` and
supports WebSockets. Projects can therefore generate correct absolute URLs
(`APP_URL=https://shop.test`).

### Node dev servers

Enable "Run a dev server" on the Node.js service (wizard or Runtime):
the script (default `dev`) runs as the container's main process and is
reachable at `https://<project>-dev.<base>` through the proxy (HMR
WebSockets included) and on a direct host port. Presets pass host/port to
Vite (`--host 0.0.0.0 --port`, default 5173), Next.js (`-H -p`, 3000) or
Nuxt (`--host --port`, 3000); "Other" only sets `HOST`/`PORT`. Run
`npm install` via Actions first; a crashing script is restarted by Docker
until it works. For Laravel + Vite set `VITE_DEV_SERVER_URL`/`APP_URL`
accordingly, or let Vite's `server.hmr` config point at the dev host name.

#### Node-only projects (Vite, Next.js, Nuxt)

PHP is optional. Start from a Vite, Next.js or Nuxt template, or pick **Node.js
application** for an empty project or a repository (or `phpVersion: "none"`
over MCP/REST), and the dev server *is* the project:

- **What answers where.** `https://<project>.<base>`, every extra domain and
  `https://<project>-dev.<base>` all reach the dev server
  (`envoryx-<project>-node:<port>`) through the proxy, HMR included. The
  node container's own host port (Domains → *Direct access*, MCP
  `directUrl`) works without DNS or the proxy.
- **The HTTP port of the web container is not published** while the dev
  server serves the project. The web container still exists (every project
  has one), but the document root defaults to the project root for Node
  projects and would otherwise expose `.env`, sources and `node_modules`
  statically on the LAN. Turn the dev server off (Runtime) and the port
  is published again with the same number.
- **Static build mode.** With the dev server off, the web server serves the
  document root statically. Set it to the build output (`dist` for Vite,
  the template does this; `out` for a Next.js `output: "export"` build) and
  run `npm run build` via Actions or the terminal. Enable **SPA fallback to
  index.html** (Web server card) so client-side routes survive a reload;
  without it unknown paths return 404. Static configs deny dotfiles
  (`/.env`, `/.git/…`) on all three web servers.
- **Production build mode.** The dev server has a mode switch (Runtime):
  *Dev server* (the default, HMR) or *Production build*. In
  production mode every container start runs the build script (default
  `build`) and then the serve script (`start`; `preview` for Vite) as the
  main process with `NODE_ENV=production`: a production-like run of a
  Next.js/Nuxt SSR app or Vite's preview server, still behind the same
  URLs. Only the serve process gets `NODE_ENV=production`; the container
  itself stays on `development`, so `npm install` from the terminal keeps
  installing devDependencies. A restart rebuilds, so the first response
  after a start takes as long as the build.
- **Debugging.** *Publish the Node.js inspector port* (Runtime, dev
  server required) publishes the inspector port (default 9229) on a host
  port of its own; the IDE section shows host, port, path mapping and
  `package.json` examples. Envoryx does not set `NODE_OPTIONS=--inspect`
  on the container on purpose: npm (a Node process itself) would grab the
  port and the debugger would attach to npm instead of your app. Start the
  inspector in your script (`NODE_OPTIONS='--inspect=0.0.0.0:9229' next
  dev`, `node --inspect=0.0.0.0:9229 node_modules/vite/bin/vite.js`) and
  attach WebStorm (*Attach to Node.js/Chrome*) or VS Code (`request:
  attach`) to the host port. Next.js opens the inspector of its server
  process one port higher (9230); publish that port when debugging server
  code. The inspector executes arbitrary code and is published on all
  host interfaces like the dev-server ports, so enable it on trusted
  networks only and turn it off when you are done.
- **Cold start.** A freshly created blank Node project has no
  `package.json` yet: the node container waits for it (log line
  `envoryx: waiting for package.json …`) instead of crash-looping, and the
  proxy shows its "web server did not respond" page until the dev server
  listens, the same page you see during a cold compile after a restart
  (the proxy waits up to 5 minutes for the first response). Templates
  scaffold and `npm install` during creation, so they are ready when the
  project turns green.
- **Vite host check.** Envoryx passes
  `__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS=.<base>` to the container: a
  single leading-dot entry that Vite suffix-matches, so `<project>.<base>`,
  `<project>-dev.<base>` and every extra domain under the base domain pass
  and foreign hosts get 403 (Vite 6.1-8.x; Vite before 8.3 reads the
  variable as one host, which is why it is never a comma-separated list).
  Domains *outside* the base domain must be added to `server.allowedHosts`
  in `vite.config`; the Domains section reminds you. Next.js and Nuxt have no
  host check.
- **Workers** run in the runtime of their preset: the Laravel/Symfony/PHP
  presets in the PHP container's image, the *npm script* (`npm run <name>`)
  and *Node.js script* (`node <file>`) presets in the Node image with the
  project home mounted. *Workers & cron* offers only the presets whose
  runtime the project has. Actions offer npm/pnpm/yarn and `node -v`; git
  clone/pull run in a one-shot container from the Node image.
- **Cron jobs** (*Workers & cron*) run any command on a schedule in the PHP, Python,
  Go, Ruby, Java, .NET or Node.js container, as the project owner in the project directory,
  through `sh -c`, with the project's environment. Pick a schedule (every few
  minutes, hourly, daily, weekly, monthly) or type a cron expression; the form
  shows the next runs. Schedules are read in Envoryx's time zone, so set `TZ` on
  the container. Jobs only run while the project is running, a run that is
  still going is not started twice, the timeout (default 10 minutes) stops
  the command, and the last 20 runs keep their output. For Laravel, either a
  *Scheduler* worker (`schedule:work`) or a cron job running `php artisan
  schedule:run` every minute, not both.
- **Adding or removing PHP later.** The Runtime section's PHP card has an
  *Enable PHP* switch on every project. Adding PHP to a Node or static
  project starts a PHP-FPM container, switches the web server to FastCGI
  and makes PHP the application (the project URL leaves the dev server;
  the SPA fallback is dropped). Removing PHP takes the PHP container and
  the PHP workers' containers down (files and worker definitions stay,
  the workers come back with PHP) and hands the project back to the dev
  server or the static document root. Over the API: `PATCH
  /api/v1/projects/{id}` with `{"php": {"version": "8.4", "config": …}}`
  adds, `{"php": {"enabled": false}}` removes.
- **Existing Node-only dev-server projects** (created before this feature
  via the unticked "Enable PHP" box or MCP `"none"`): the node and web
  containers are recreated once at the next start (new command wrapper,
  unpublished port); from then on `<project>.<base>` reaches the dev server.

A **static site** (no PHP, no Node, no Python, Go, Ruby, Java or .NET server) is the same web
container alone: pick **Static site** in the wizard; Envoryx writes a
starter `index.html` unless you clone a repository.

### Python projects (Django, Flask, FastAPI)

Start from a Python template, or pick **Python application** for an empty
project or a repository (or enable Python on any project under Runtime). The Python container
(`envoryx-<project>-python`, image `ghcr.io/envoryx/envoryx-python:<3.x>`
with pip, uv, git and the build dependencies common wheels need) runs as
`PUID:PGID` with the project directory at `/var/www/html` and the
persistent project home at `/home/envoryx` (pip and uv caches). The
project's virtual environment is `/var/www/html/.venv`: the container's
`PATH` starts with `.venv/bin`, so `python`, `pip`, `gunicorn`, `uvicorn` …
resolve to it as soon as it exists; `~/.local/bin` (`pip install --user`)
comes next.

- **Application server.** *Run the application server* makes the preset's
  command the container's main process, restarted automatically and
  published on a host port of its own. Presets: **Django** (`python
  manage.py runserver 0.0.0.0:<port>`; production mode `gunicorn
  <app> --bind …` with the WSGI module, e.g. `config.wsgi:application`),
  **Flask** (`flask --app <app> run --debug`; production mode gunicorn),
  **FastAPI / ASGI** (`uvicorn <app> --reload`; production mode without
  reload; Starlette, Litestar and any ASGI app work the same way), **WSGI**
  (`gunicorn <app> --reload`) and **Other** (`python -m <module>` reading
  `HOST`/`PORT`). The application is given as `module:attribute`
  (`main:app`, `app:app`). Production mode needs gunicorn/uvicorn in the
  `.venv` (the templates install them).
- **Routing.** Without PHP the Python server is the application: the proxy
  routes `https://<project>.<base>` and every extra domain to
  `envoryx-<project>-python:<port>`, the web container's host port stays
  unpublished (the project root with `.env` and sources is never served),
  and the Domains section's *Direct access* is the Python host port. A Node dev
  server next to Python keeps `<project>-dev.<base>` and its own host port -
  the usual Django/FastAPI backend plus Vite frontend. Next to PHP the
  Python server only has its host port; PHP stays the application.
- **Cold start.** A blank Python project has nothing to run yet: the
  container waits for the entry file (`manage.py` for Django, else
  `<module>.py` or `<module>/`, log line `envoryx: waiting for …`) instead
  of crash-looping. Pick a template, clone a repository or set the project
  up from the Python terminal; the container picks it up within seconds.
- **Templates.** *Django* (`django-admin startproject config .`, settings
  patched for the proxy (`ALLOWED_HOSTS = ["*"]`, `SECURE_PROXY_SSL_HEADER`,
  `USE_X_FORWARDED_HOST`, `CSRF_TRUSTED_ORIGINS` from the environment) and
  `dj-database-url` reading the injected `DATABASE_URL`; psycopg and
  mysqlclient installed), *Flask* (`app.py`) and *FastAPI* (`main.py`,
  docs at `/docs`). Each creates the `.venv`, installs the packages and
  pins them with `pip freeze > requirements.txt`, so *pip install* from
  Actions reproduces the environment after a fresh clone.
- **Actions and workers.** Actions: `python --version`, `python -m venv
  .venv`, `pip install -r requirements.txt` (creates the venv when
  missing), `pip freeze`, `uv sync`/`uv lock` (with a `pyproject.toml`),
  Django `migrate`, `makemigrations`, `collectstatic`, `check` and `flush`
  (with a `manage.py`). Worker presets in the Python image: *Python script*
  (`python <file>`), *Python module* (`python -m <module>`), *manage.py
  command* (`rqworker`, `qcluster`, …), *Celery worker* and *Celery beat*
  (`celery -A <app> …`).
- **Debugging.** *Publish the debugpy port* (Runtime; with or without
  the server, for a script or test run started in the terminal) publishes
  port 5678 on a host port; the IDE section shows host, port and path
  mapping plus command lines that start debugpy in front of the usual
  servers. Only the port is published: `pip install debugpy` in the
  `.venv` and start it yourself (`python -m debugpy --listen
  0.0.0.0:5678 manage.py runserver …`), then attach VS Code (`type:
  debugpy`, `request: attach`). PyCharm's *Python Debug Server* works the
  other way round (the IDE listens); use its `pydevd-pycharm` snippet with
  your workstation's address.
- **Adding or removing Python later.** The Runtime section's Python card has
  an *Enable Python* switch; removing it takes the Python container and the
  Python workers' containers down; files, `.venv` and worker definitions
  stay. Over the API: `PATCH /api/v1/projects/{id}` with `{"python":
  {"enabled": true, "version": "3.13", "server": true, "preset": "asgi",
  "app": "main:app"}}` adds or changes, `{"python": {"enabled": false}}`
  removes.

### Go projects (net/http, Gin, Echo)

Start from a Go template, or pick **Go application** for an empty project or
a repository (or enable Go on any project under Runtime). The Go container (`envoryx-<project>-go`,
image `ghcr.io/envoryx/envoryx-go:<1.x>`: the official
`golang:<v>-bookworm` image plus [air](https://github.com/air-verse/air),
Delve and gotestsum, `GOTOOLCHAIN=local`) runs as `PUID:PGID` with the
project directory at `/var/www/html` and the project home at
`/home/envoryx` (`GOPATH=/home/envoryx/go`, so tools installed with `go
install` stay and are on `PATH`). The module cache (`GOMODCACHE`) and the
build cache (`GOCACHE`) live in the shared package cache, so a module is
downloaded once for all projects.

- **Server.** *Build and run the server* makes the container build the
  main package (`.` or a path like `./cmd/server`) and run it as its main
  process, restarted automatically and published on a host port of its
  own. The server reads its port from `$PORT` (default 8080; `HOST` is
  `0.0.0.0`). **Development** mode runs air, which rebuilds and restarts
  the server on every change of a `.go` file. **Production build** builds
  once at container start and runs the binary; restart the project after
  changes. Binaries are built to `/tmp/envoryx-go` inside the container,
  never into the project directory. A `.air.toml` in the project replaces
  Envoryx's air settings entirely: build command, output directory (air's
  default is `./tmp` in the project) and, with Delve, how the binary starts:
  its `build.full_bin` then has to run `dlv exec` itself. `GIN_MODE` follows
  the mode (`debug`/`release`).
- **Routing.** Without PHP and without a Python server the Go server is the
  application: the proxy routes `https://<project>.<base>` and every extra
  domain to `envoryx-<project>-go:<port>`, the web container's host port
  stays unpublished, and *Direct access* is the Go host port. Next to PHP
  or a Python server the Go server only has its host port, as an API next to
  the main application. A Node dev server next to Go keeps
  `<project>-dev.<base>`.
- **Cold start.** Until the project has a `go.mod` the server container waits (log
  line `envoryx: waiting for go.mod …`) instead of crash-looping. Pick a
  template, clone a repository or run `go mod init` in the Go terminal.
- **Templates.** *Go (net/http)* (standard library only), *Gin* and *Echo*.
  Each runs `go mod init app` and writes a `main.go` with a start route
  and a health check (`/healthz`) that listens on `$HOST:$PORT`; Gin and Echo
  fetch the framework (`go get`, `go mod tidy`).
- **Actions, tests and workers.** Actions: `go version`, `go build ./...`,
  `go vet ./...`, `gofmt -l .`, `go mod tidy`, `go mod download` and `go
  generate ./...`. The Tests section runs `go test ./...` through gotestsum
  (with a JUnit report, so failures show per test). Worker preset *Go
  program* builds a package and runs the binary (not `go run`, which would
  swallow the stop signal); cron jobs run in the Go container like in the
  others.
- **Debugging.** *Debug with Delve* runs the server under a headless Delve
  (`dlv exec --headless --accept-multiclient --continue`, port 2345 by
  default) built without optimisations, and publishes that port on a host
  port; breakpoints survive every rebuild. Attach GoLand (*Run → Edit
  Configurations → Go Remote*) or VS Code (`type: go`, `request: attach`,
  `mode: remote`, `substitutePath` from your folder to `/var/www/html`)
  with the host and port from the IDE section. Without the server only the port
  is published, for `dlv test --headless --listen=:2345 ./pkg/...` or `dlv
  debug` started in the Go terminal. Delve has no authentication: whoever
  reaches the port can run any code in the container, and with the server
  it listens as long as the switch is on, not only while an IDE is
  attached. Switch it off when you are not debugging, and do not enable it
  on a Docker host reachable from untrusted networks.
- **Adding or removing Go later.** The Runtime section's Go card has an *Enable
  Go* switch; removing it takes the Go container and the Go workers'
  containers down, while files and worker definitions stay. Over the API:
  `PATCH /api/v1/projects/{id}` with `{"go": {"enabled": true, "version":
  "1.27", "server": true, "package": "./cmd/server"}}` adds or changes,
  `{"go": {"enabled": false}}` removes. The CLI takes `--go <version>`,
  `--go-server` and `--go-package`.

### Ruby projects (Rails, Sinatra, Rack)

Start from a Ruby template, or pick **Ruby application** for an empty project
or a repository (or enable Ruby on any project under Runtime). The Ruby container (`envoryx-<project>-ruby`,
image `ghcr.io/envoryx/envoryx-ruby:<3.x|4.x>`: the official
`ruby:<v>-slim-bookworm` image plus the build dependencies of the common
native gems (pg, mysql2, sqlite3, psych) and the debug gem) runs as
`PUID:PGID` with the project directory at `/var/www/html` and the project
home at `/home/envoryx`. Gems go to `GEM_HOME=/home/envoryx/.gem/ruby`, so they
stay across container recreates and the project directory holds no
`vendor/bundle`, and Bundler's download cache lives in the shared package
cache. The image has no Node.js: `jsbundling-rails`/`cssbundling-rails`
builds run in a Node service next to Ruby; the Rails template uses importmap
and needs none.

- **Server.** *Run the Ruby server* makes the preset's server the container's
  main process, restarted automatically and published on a host port of its
  own. **Rails** runs `bin/rails server` in development mode (Rails reloads
  changed code itself) and `bundle exec puma` in production mode; **Rack**
  runs Puma on `config.ru` in both modes (Sinatra, Roda, Hanami; code
  reloading is the framework's, e.g. `sinatra/reloader`). The server listens
  on `$PORT` (3000 for Rails, 9292 for Rack; `HOST`/`BINDING` are
  `0.0.0.0`). `RAILS_ENV`, `RACK_ENV`, `APP_ENV` and `HANAMI_ENV` follow the
  mode, and the workers get the same. In development Envoryx sets
  `RAILS_DEVELOPMENT_HOSTS=.<base>`, so Rails' host authorization lets the
  project's host names through. Production mode is Rails' production
  environment: it needs `config/master.key` (or `SECRET_KEY_BASE` as a
  project variable), precompiled assets (*rails assets:precompile*) and the
  databases its `database.yml` names for production; Rails serves `public/`
  itself (`RAILS_SERVE_STATIC_FILES`), and a production config with
  `force_ssl` redirects to https.
- **Bundle.** Before the server starts it waits for the `Gemfile` (and
  `bin/rails` or `config.ru`), then runs `bundle install` when `bundle check`
  finds the bundle incomplete, as after a clone, a changed `Gemfile` or a Ruby
  upgrade. While the install fails the container says so in the log and
  retries every 30 seconds. The workers do the same before they start.
- **Database.** Envoryx injects `DATABASE_URL`, and Rails merges it into
  `config/database.yml`. For PostgreSQL the Ruby containers get it as
  `postgresql://` (Envoryx names the scheme `pgsql` elsewhere, which Active
  Record does not know). An additional database `analytics` arrives as
  `ANALYTICS_DATABASE_URL`, which Rails' multi-database setup picks up for a
  database named `analytics` in `database.yml`.
- **Routing.** Without PHP and without a Python or Go server the Ruby server
  is the application: the proxy routes `https://<project>.<base>` and every
  extra domain to `envoryx-<project>-ruby:<port>`, the web container's host
  port stays unpublished, and *Direct access* is the Ruby host port. Next to
  PHP or a Python or Go server it only has its host port. A Node dev server
  next to Ruby keeps `<project>-dev.<base>`.
- **Templates.** *Rails* (`rails new` with Hotwire and importmap), *Rails
  (API only)* (`rails new --api`), both named after the project and set up
  for its database (PostgreSQL, MySQL, MariaDB; SQLite without one), and
  *Sinatra* (`app.rb`, `config.ru`, a `Gemfile` with Puma, `sinatra-contrib`
  for the reloader and the debug gem). The Rails templates take a few
  minutes the first time, while the native gems compile; run *rails
  db:prepare* from Actions afterwards.
- **Actions, tests and workers.** Actions: `ruby --version`, `bundle
  install`/`update`/`outdated`, `rubocop` (with a `.rubocop.yml`), and for
  Rails `db:prepare`, `db:migrate`, `db:rollback`, `db:seed`, `routes`,
  `assets:precompile`, `tmp:clear` and `about`. The Tests section runs `bundle
  exec rspec` (with a `spec/` directory and rspec in `Gemfile.lock`; with
  `rspec_junit_formatter` in the bundle failures show per test) and `bin/rails
  test`, in the test environment and against `<database>_test` on the
  project's database server: Active Record merges `DATABASE_URL` into every
  environment, so a test run would otherwise load its fixtures into the
  development database and empty its tables. Envoryx creates the test
  database (as the administrator, since the MySQL/MariaDB project login may not
  create databases) before the run; Rails loads the schema. Worker presets:
  *Solid Queue* (`bin/jobs start`), *GoodJob* (`good_job start`, optional
  queues), *Sidekiq* (optional queues; add Redis, and `REDIS_URL` is injected),
  *Rake task* and *Ruby script*. Cron jobs run in the Ruby container like in
  the others.
- **Debugging.** *Debug with rdbg* runs the server under `rdbg --open
  --nonstop` (the debug gem, port 12345 by default) and publishes that port
  on a host port; when `Gemfile.lock` locks the debug gem (Rails and the
  Sinatra template do) the bundle's rdbg runs it. Attach VS Code with the
  *VSCode rdbg Ruby Debugger* extension (`type: rdbg`, `request: attach`,
  `debugPort: "<host>:<port>"`, `localfsMap: "/var/www/html:${workspaceFolder}"`)
  or a terminal (`rdbg -A <host> <port>`) with the host and port from the IDE
  section. RubyMine's *Ruby remote debug* speaks only `ruby-debug-ide`, not the
  debug gem: in RubyMine, add the Ruby container as an SSH remote interpreter
  (user `<project>.ruby`, see *IDE integration*) and debug with RubyMine's own
  debugger; the rdbg switch isn't needed for that. Without the server only the
  port is published, for an `rdbg --open --host=0.0.0.0 --port=12345 -c --
  bin/rails test` started in the Ruby terminal. rdbg has no authentication:
  whoever reaches the port can run any code in the container, and with the
  server it listens as long as the switch is on. Switch it off when you are
  not debugging, and do not enable it on a
  Docker host reachable from untrusted networks.
- **Adding or removing Ruby later.** The Runtime section's Ruby card has an
  *Enable Ruby* switch; removing it takes the Ruby container and the Ruby
  workers' containers down, while files and worker definitions stay. Over the API:
  `PATCH /api/v1/projects/{id}` with `{"ruby": {"enabled": true, "version":
  "3.4", "server": true, "preset": "rails"}}` adds or changes, `{"ruby":
  {"enabled": false}}` removes. The CLI takes `--ruby <version>`,
  `--ruby-server` and `--ruby-preset rails|rack`.

### Java projects (Spring Boot, Quarkus)

Start from a Java template, or pick **Java application** for an empty project
or a repository (or enable Java on any project under Runtime). The Java container (`envoryx-<project>-java`,
image `ghcr.io/envoryx/envoryx-java:<17|21|25>`: Eclipse Temurin
`<v>-jdk-noble` plus Maven, Gradle, git and socat; only the LTS releases) runs
as `PUID:PGID` with the project directory at `/var/www/html` and the project
home at `/home/envoryx`. Maven's repository and Gradle's caches live in the
shared package cache, so a dependency is downloaded once for every project.

- **Build tool.** A `pom.xml` means Maven, else a `build.gradle` or
  `build.gradle.kts` means Gradle. The project's wrapper (`mvnw`, `gradlew`)
  wins over the image's Maven or Gradle and runs through `sh`, so a wrapper
  that lost its executable bit in a zip or a Windows checkout still works.
- **Server.** *Run the Java server* makes the preset's server the container's
  main process, restarted automatically and published on a host port of its
  own. Before it starts it waits for the build file. **Spring Boot** runs
  `mvn spring-boot:run` or `gradle bootRun` in development mode (DevTools
  restarts the application when compiled classes change, so run `mvn compile`
  in the Java terminal or let the IDE build after editing); **Quarkus** runs
  `mvn quarkus:dev` or `gradle quarkusDev`, which recompiles on the next
  request. Production mode builds the project once without the tests
  (`mvn package`, `gradle build`) and then runs the jar: Quarkus'
  `target/quarkus-app/quarkus-run.jar`, else the newest jar in `target/` or
  `build/libs/` (not the `-plain`, `-sources`, `-javadoc` or `-tests` ones).
  The **Jar** preset has no dev mode: it builds and runs a jar, the one you
  name (`build/libs/app.jar`) or the newest. The server listens on port 8080
  by default; `SERVER_PORT`, `QUARKUS_HTTP_PORT`, `MICRONAUT_SERVER_PORT` and
  `PORT` carry the port, and the matching host variables are `0.0.0.0`.
- **Database.** Envoryx injects `SPRING_DATASOURCE_URL`/`_USERNAME`/`_PASSWORD`
  for Spring Boot and `QUARKUS_DATASOURCE_DB_KIND`/`_JDBC_URL`/`_USERNAME`/`_PASSWORD`
  for Quarkus (PostgreSQL, MySQL, MariaDB), plus `JDBC_URL` for anything else;
  an additional database `analytics` arrives as `ANALYTICS_JDBC_URL`.
  MongoDB, Redis and Mailpit get the Spring and Quarkus variables too
  (`SPRING_DATA_MONGODB_URI`, `SPRING_DATA_REDIS_URL`, `QUARKUS_REDIS_HOSTS`,
  `SPRING_MAIL_HOST`, `QUARKUS_MAILER_HOST` …). Quarkus Dev Services are
  switched off (`QUARKUS_DEVSERVICES_ENABLED=false`): the project's database
  is the one the application uses, not a container Quarkus would start itself.
- **Routing.** Without PHP and without a Python, Go or Ruby server the Java
  server is the application: the proxy routes `https://<project>.<base>` and
  every extra domain to `envoryx-<project>-java:<port>`, and *Direct access*
  is the Java host port. Next to one of the others it only has its host port.
- **Templates.** *Spring Boot* (from start.spring.io: Spring Web, Actuator and
  DevTools, with JPA and the driver when the project has a database) and
  *Quarkus REST* (from code.quarkus.io: REST with Jackson and SmallRye Health,
  with Hibernate ORM Panache and the driver when it has one). Both are Maven
  projects named after the project, on the project's Java version, and are
  built once with their wrapper, which takes a few minutes the first time
  while Maven fills the cache. With a database the Quarkus template sets
  `%dev.quarkus.hibernate-orm.schema-management.strategy=update` in
  `application.properties`, so Hibernate creates the tables in dev mode and
  keeps the data across live reloads.
- **Actions, tests and workers.** Actions: `java -version`; for Maven
  `package`, `clean`, `dependency:tree` and
  `versions:display-dependency-updates`; for Gradle `build`, `clean`,
  `dependencies` and `tasks`. The Tests section runs `mvn test` or `gradle test`
  (filter with `-Dtest` or `--tests`) and shows the result per test from the
  JUnit reports. The run points `SPRING_DATASOURCE_URL`,
  `QUARKUS_DATASOURCE_JDBC_URL`, `JDBC_URL` and `DATABASE_URL` at
  `<database>_test`, which Envoryx creates first, so a test with `create-drop`
  cannot empty the development database. Worker presets: *Jar file* (`java
  -jar <file>`) and *Build tool goal* (`mvn <goal>` or `gradle <task>`, e.g.
  `exec:java -Dexec.mainClass=com.example.Worker`). Cron jobs run in the Java
  container like in the others.
- **Debugging.** *Debug with JDWP* starts the server's JVM with a JDWP agent
  (port 5005 by default, `suspend=n`) and publishes that port on a host
  port. For `spring-boot:run` the agent goes into the forked application JVM,
  for `bootRun` an init script (`/opt/envoryx/jdwp.gradle`) adds it, and
  Quarkus dev mode gets `-Ddebug`. Attach IntelliJ IDEA with *Run → Remote JVM
  Debug* or VS Code (`type: java`, `request: attach`) with the host and port
  from the IDE section. Without the server only the port is published, for a JVM
  started in the Java terminal with
  `-agentlib:jdwp=transport=dt_socket,server=y,suspend=n,address=*:5005`.
  JDWP has no authentication: whoever reaches the port can run any code in
  the JVM. Switch it off when you are not debugging, and do not enable it on
  a Docker host reachable from untrusted networks.
- **Adding or removing Java later.** The Runtime section's Java card has an
  *Enable Java* switch; removing it takes the Java container and the Java
  workers' containers down, while files and worker definitions stay. Over the API:
  `PATCH /api/v1/projects/{id}` with `{"java": {"enabled": true, "version":
  "25", "server": true, "preset": "spring-boot"}}` adds or changes, `{"java":
  {"enabled": false}}` removes. The CLI takes `--java <version>`,
  `--java-server` and `--java-preset spring-boot|quarkus|jar`.

### .NET projects (ASP.NET Core, Blazor, worker services)

Start from a .NET template, or pick **.NET application** for an empty project
or a repository (or enable .NET on any project under Runtime). The .NET container (`envoryx-<project>-dotnet`,
image `ghcr.io/envoryx/envoryx-dotnet:<8|10>`: the official SDK image
`mcr.microsoft.com/dotnet/sdk:<v>.0-noble` plus `dotnet-ef`, the netcoredbg
debugger, git and socat; only the LTS releases, and .NET 8 reaches its end of
support on 2026-11-10) runs as `PUID:PGID` with the project directory at
`/var/www/html` and the project home at `/home/envoryx`. NuGet's packages
folder and HTTP cache live in the shared package cache, so a package is
downloaded once for every project.

- **Which project.** The server runs the project file you set (a relative
  path such as `src/Shop/Shop.csproj`). Left empty, it takes the one `.csproj`,
  `.fsproj` or `.vbproj` at the top of the project, else the one ASP.NET Core
  or worker project (`Sdk="Microsoft.NET.Sdk.Web"` or `.Worker`) up to four
  levels down; with several it stops and says so in the log. A common layout
  is `src/` and `tests/` with a solution at the top. A test project that sits
  inside the application's own directory is compiled into the application
  (standard .NET globbing), so keep it outside or exclude it with
  `<Compile Remove="tests/**" />`.
- **Server.** *Run the .NET server* makes the preset's server the container's
  main process, restarted automatically and published on a host port of its
  own. Before it starts it waits for a project file. **ASP.NET Core** (Web
  API, MVC, Razor Pages, Blazor) runs `dotnet watch --non-interactive` in
  development mode: code changes are applied with hot reload, and when an
  edit can't be applied the application restarts. `--urls
  http://0.0.0.0:<port>` wins over the launch profile's `applicationUrl`,
  which only listens on localhost. Production mode runs `dotnet publish -c
  Release` into `bin/envoryx-publish` and starts the DLL from there, so
  `wwwroot` and the `appsettings` files come from the published output. The
  **DLL** preset has no dev mode: it publishes and runs the DLL you name, or
  the application the publish produced (worker services, console hosts). The
  server listens on port 8080 by default; `ASPNETCORE_HTTP_PORTS` carries it
  (it stays empty under `dotnet watch`, where `--urls` does the job),
  `ASPNETCORE_ENVIRONMENT` and `DOTNET_ENVIRONMENT` are `Development` or
  `Production`, and `ASPNETCORE_FORWARDEDHEADERS_ENABLED=true` makes the
  application trust the proxy's `X-Forwarded-*` headers, so redirects and
  links keep the project URL's scheme and host.
- **Database.** Envoryx injects `ConnectionStrings__DefaultConnection` for the
  primary PostgreSQL, MySQL or MariaDB database, which
  `builder.Configuration.GetConnectionString("DefaultConnection")` reads. It's
  in the ADO.NET form Npgsql (`Host=database;Port=5432;Database=…;Username=…;Password=…`)
  and the MySQL providers (`Server=database;Port=3306;Database=…;User ID=…;Password=…`)
  take. An additional database `analytics` arrives as
  `ConnectionStrings__analytics`; MongoDB as `ConnectionStrings__MongoDB` (the
  URI) and Redis as `ConnectionStrings__Redis` (`redis:6379`, with
  `,password=…` for an external one with a password).
- **Routing.** Without PHP and without a Python, Go, Ruby or Java server the
  .NET server is the application: the proxy routes `https://<project>.<base>`
  and every extra domain to `envoryx-<project>-dotnet:<port>`, and *Direct
  access* is the .NET host port. Blazor's interactive components use a
  WebSocket, which the proxy passes through.
- **Templates.** *ASP.NET Core Web API*, *ASP.NET Core MVC*, *Blazor Web App*
  and *ASP.NET Core Razor Pages* come from `dotnet new` (`webapi`, `mvc`,
  `blazor`, `webapp`) with `--no-https`, since the Envoryx proxy terminates
  TLS; they need no network until the NuGet restore. The project is named
  after the project's slug and built once. With a PostgreSQL, MySQL or MariaDB
  database the Web API template adds EF Core: the provider (Npgsql, or
  Oracle's `MySql.EntityFrameworkCore` for MySQL and MariaDB, since Pomelo has
  no release for EF Core 10) and `Microsoft.EntityFrameworkCore.Design` in the
  SDK's major version, pinned to the versions that resolved, plus
  `AppDatabase.cs` with a context, a sample `Todo` entity and `GET`/`POST
  /todos`. It creates the tables with `EnsureCreated` on the first start;
  switch to migrations (`dotnet ef migrations add Initial`, then the `dotnet
  ef database update` action) once the model settles.
- **Actions, tests and workers.** Actions: `dotnet --info`, `restore`,
  `build`, `clean`, `format` and `list package --outdated` (they need a
  solution or project file at the top), `dotnet ef database update` and
  `dotnet ef migrations list` (a project file at the top, referencing
  `Microsoft.EntityFrameworkCore.Design`). The Tests section runs `dotnet test`
  once a test project (xUnit, NUnit, MSTest, TUnit or the test SDK) exists:
  the solution at the top, or the one test project; several test projects
  without a solution file need one (`dotnet new sln`, `dotnet sln add`). Filter
  with `--filter` (`FullyQualifiedName~OrderTests`); the results per test,
  with file and line, come from the TRX report. The run points
  `ConnectionStrings__DefaultConnection` at `<database>_test`, which Envoryx
  creates first, so integration tests (`WebApplicationFactory`) don't touch
  the development data. A project on Microsoft.Testing.Platform (chosen in
  `global.json`) runs without the report and the filter; only the exit code
  counts there. Worker presets: *Project* (publishes the project inside the
  worker's container and runs it, so the stop signal reaches the application)
  and *DLL* (`dotnet <file>`). Cron jobs run in the .NET container like in the
  others.
- **Debugging.** There's no debug port. The IDEs start a debugger inside the
  container over the SSH user `<project>.dotnet` and attach to the running
  application (under `dotnet watch` it's the process named after the project,
  not `dotnet watch` itself). VS Code with the C# extension uses netcoredbg
  (`/usr/local/bin/netcoredbg`) through `pipeTransport`; Microsoft's own
  `vsdbg` may only be used from Microsoft's IDEs, so the image doesn't ship
  it. Store your public key under *Settings → SSH keys* first, since the pipe
  can't answer a password prompt. A `.vscode/launch.json`:

  ```json
  {
    "version": "0.2.0",
    "configurations": [
      {
        "name": "Attach to Envoryx",
        "type": "coreclr",
        "request": "attach",
        "processId": "${command:pickRemoteProcess}",
        "pipeTransport": {
          "pipeProgram": "ssh",
          "pipeArgs": ["-p", "2222", "shop.dotnet@<host>"],
          "pipeCwd": "${workspaceFolder}",
          "debuggerPath": "/usr/local/bin/netcoredbg"
        },
        "sourceFileMap": { "/var/www/html": "${workspaceFolder}" }
      }
    ]
  }
  ```

  Rider and Visual Studio attach through *Attach to Remote Process* over an
  SSH connection to the same user.
- **Adding or removing .NET later.** The Runtime section's .NET card has an
  *Enable .NET* switch; removing it takes the .NET container and the .NET
  workers' containers down, while files and worker definitions stay. Over the
  API: `PATCH /api/v1/projects/{id}` with `{"dotnet": {"enabled": true,
  "version": "10", "server": true, "preset": "aspnetcore"}}` adds or changes
  (also `mode`, `project`, `dll`, `port`), `{"dotnet": {"enabled": false}}`
  removes. The CLI takes `--dotnet <version>`, `--dotnet-server` and
  `--dotnet-preset aspnetcore|dll`.

### Bare metal

Outside Docker the proxy dials the project's published port
(`127.0.0.1:<port>`) instead of joining the project network. Binding 80/443
needs `setcap cap_net_bind_service=+ep ./envoryx` or other addresses
(`ENVORYX_PROXY_HTTP=:8080`).

### Rules: redirects, headers, CORS and access

*Domains → Rules* tells the proxy what to do with a project's requests before
the application sees them. The rules apply to every host name of the project
(the default name, extra domains, the Node dev server name) and to a share -
not to the directly published port, which bypasses the proxy.

- **Allowed addresses:** one address or network per line
  (`192.168.1.0/24`, `10.8.0.5`, IPv6 too); everybody else gets a 403 page
  naming their address. The address is the one the connection comes from:
  behind another reverse proxy (SWAG, Nginx Proxy Manager) that is the other
  proxy's address. A project with an allowlist cannot be shared.
- **User name and password:** HTTP basic authentication in front of the whole
  project, for a staging copy or a share. The password is kept as a bcrypt
  hash; saving without a new password keeps it. The application does not see
  these credentials.
- **Redirects:** tried in order, the first match answers (302 unless 301, 307
  or 308 is chosen). `From` is a path or a prefix ending in `*`; a `To` ending
  in `*` gets the rest of the path, and the query string is passed on.
  `www.shop.test` `/*` → `https://shop.test/*` makes one host name canonical;
  `/blog/*` → `/news/*` moves a section.
- **Response headers:** set on every answer of the application
  (`X-Robots-Tag: noindex`, `Content-Security-Policy`, …); an empty value
  removes a header such as `X-Powered-By`.
- **CORS:** for a frontend on another host name calling the project's API:
  the proxy answers preflight requests from the listed origins
  (`https://app.test`, `https://*.shop.test` or `*`) and adds the CORS headers
  to the answers, replacing the application's own. With *Allow cookies and
  credentials* the origins have to be listed.

The order is: allowlist, https redirect, CORS preflight, password,
redirects, application. The API: `PUT /projects/{id}/proxy-rules` (`admin`)
replaces all of a project's rules; the project shows them as `proxyRules`
without the password. Duplicating a project copies them.

## Project variables and .env files

A project's variables (*Environment* section, or *Advanced settings → Environment* in the wizard)
reach every container of the project and win over what Envoryx sets for the
services. **Import .env** reads a `.env` file (pasted or chosen) in the form
Laravel, Symfony and docker compose write (`export`, comments, single and
double quotes, `${OTHER}` references to keys above) and lists what it found:

- new variables and changed values are selected; values that stay the same
  are not;
- variables Envoryx sets for one of the project's services (`DB_HOST`,
  `DB_PASSWORD`, `REDIS_HOST`, `MAIL_*`, `ANALYTICS_DB_*` …) are left out
  unless picked, since the old setup's `DB_HOST=127.0.0.1` would otherwise point
  the application away from the project database;
- names Envoryx refuses (lower case, `MARIADB_*`, `POSTGRES_*`, `ENVORYX_*` …)
  and values with line breaks are shown but cannot be imported;
- secrets are recognised by their name (`*_PASSWORD`, `*_SECRET`, `*_TOKEN`,
  `APP_KEY`, `*_KEY` …) or by a password in a URL, and masked; each can be
  switched.

Nothing is stored until the variables are saved. **Export .env** downloads the
variables as a `.env` file, secrets in plain text; the variables Envoryx sets
for the services are not part of it.

## Importing an existing website

*New project → Existing website* takes a site that already exists
somewhere else (an old shared host, a backup, an FTP download) and makes a
project of it. Upload the files as a ZIP or tar.gz archive (a single folder
around them such as `public_html/` or `httpdocs/` is left out) and, optionally,
a database dump (`.sql` or `.sql.gz`). Envoryx reads the archive and fills the
rest of the wizard with what it recognised; everything stays editable.

| Site | Recognised by | Suggestion |
|------|---------------|------------|
| WordPress | `wp-config.php`, `wp-includes/version.php` | PHP up to what the WordPress version supports, `mysqli`, MariaDB |
| Laravel | `artisan` + `laravel/framework` | docroot `public`, database from `.env` |
| Symfony | `bin/console` + `symfony/framework-bundle` | docroot `public` (`web` for Symfony 2/3), database from `DATABASE_URL` |
| Drupal | `core/lib/Drupal.php` (also below `web/`), Drupal 7 | docroot, PHP by major version |
| TYPO3 | `typo3/sysext`, `typo3/cms-core` | docroot `public` in Composer mode |
| Joomla | `configuration.php` with `JConfig` | PHP by version, `mysqli` |
| Shopware, Craft CMS, other Composer apps | `composer.json` | PHP from `require.php`, `ext-*` extensions |
| Plain PHP | `.php` files | docroot where `index.php` is, the files that connect to a database |
| Static site, Node.js, Python, Go, Ruby, Java, .NET | `index.html`, `package.json`, `manage.py`/`requirements.txt`, `go.mod`, `Gemfile`, `pom.xml`/`build.gradle`, `.sln`/`.slnx`/`.csproj` at the top | the matching runtime |

The PHP version is the newest one Envoryx offers that `composer.json` and the
CMS version allow. Code that calls functions PHP 8 removed (`create_function`,
`each`) gets PHP 7.4; code that still uses `mysql_*` or `ereg` gets a warning
- no PHP Envoryx offers runs it unchanged. A `.htaccess` in the document root
suggests Apache, the only web server that reads it (not for Laravel, Symfony,
Shopware and Craft, whose front controller works everywhere). The database
follows the dump's header (a MySQL 8 dump needs MySQL, it uses collations
MariaDB does not have) or, without a dump, the site's configuration.

**Adapting the configuration** (on by default) wires the site to the project
database:

- WordPress: `DB_NAME`, `DB_USER`, `DB_PASSWORD` and `DB_HOST` in
  `wp-config.php` read the variables Envoryx injects, and `WP_HOME`/`WP_SITEURL`
  follow the address the site is opened with. Links stored in the database
  still name the old address; replace them with a search-and-replace plugin or
  `wp search-replace`.
- Drupal (`settings.php`) and TYPO3 (`additional.php` or
  `AdditionalConfiguration.php`) get a block at the end that sets the connection
  from the injected variables.
- Joomla's `configuration.php` gets the connection written out (its class
  cannot read the environment), an empty `live_site` and `log_path`/`tmp_path`
  below the site.
- Laravel, Symfony and Shopware need nothing: the injected `DB_*` and
  `DATABASE_URL` override `.env`. The configuration caches of the old server
  (`bootstrap/cache/config.php`, `var/cache`) are removed.

Every changed file is kept next to itself as `<name>.envoryx-original.php`,
starting with a line that answers 404, so the web server never hands the old
credentials out. For plain PHP sites the wizard names the files that open a
connection; change those by hand (host `database`, the rest under *Database*).
Without adapting, the files stay exactly as uploaded.

**The dump** is imported after the containers are created; the database
container is started for it if the project isn't running. Statements that tie it to
the old server are left out: `CREATE DATABASE`/`USE` (mysqldump `--databases`),
`\connect`, `OWNER TO`, `GRANT`/`REVOKE` and roles (pg_dump). pg_dump's custom
format is refused before anything is created; export plain SQL
(`pg_dump --format=plain`). A failed import rolls the project back (the
directory Envoryx filled is emptied again), and the upload stays for another try.

Uploads wait in `<backups>/.site-imports/` for 24 hours (an archive may be up to
20 GiB and unpack to at most 64 GiB) and are removed once the project exists.
The project directory must be empty or not exist yet. The API behind it:
`POST /site-imports` (multipart, fields `site` and `database`) returns the
analysis, `GET`/`DELETE /site-imports/{id}`, and `POST /projects` with
`"import": {"id": "…", "adaptConfig": true}` creates the project; both need an
`admin` token. From the command line:

```sh
envoryx import ./old-blog "Old Blog" --db old-blog.sql.gz --start
envoryx import backup.zip --php 8.2 --web nginx --no-adapt
envoryx import ./site --dry-run          # only show what Envoryx recognises
```

`envoryx import` packs a folder on the fly (files, folders and symlinks, `.git`
included) or sends an archive as it is, prints what was recognised and creates
the project with that suggestion; `--php`, `--database TYPE[:VERSION]` (`none`
for no database), `--web`, `--docroot` and `--path` override it.

## Resource limits

A project's **Resources** section caps what its containers may use, so a runaway
queue worker or Node process cannot take the whole server:

- **Application containers** (web server, PHP, Node.js, Python, Go, Ruby, Java, .NET, every worker)
  and **services** (database, Redis, Memcached, Mailpit, RabbitMQ, the search
  engines, object storage) each get CPU cores (e.g. `1.5`) and memory. Docker
  limits containers one by one, so the numbers apply to *each* container of
  the group: a project with PHP, Node and two workers may use four times the
  application limit. A project-wide total would need a cgroup on the host that
  Envoryx cannot manage from its container.
- **Processes per container** (default 4096) stops fork bombs and worker pools
  that keep spawning. It applies to every container, also without other
  limits.
- Empty means no limit, which is also where every project starts. OpenSearch
  needs at least 1.5 GiB.

Changes reach running containers right away through `docker update`; only
removing a CPU or memory limit recreates the containers concerned (Docker
cannot lift one in place). Swap is not added on top of the memory limit. The
card shows each running container's CPU and memory against its limit.

When a container reaches its memory limit, the kernel ends a process in it -
the main process (the container restarts) or just a child such as a php-fpm
worker or a queue job, while the container keeps running. Envoryx follows
Docker's OOM events: the project shows a warning for a day ("php ran out of
memory at 14:03 (limit 512 MiB); a process was killed") and a notification
goes out (event `project.oom`, on by default). The limits also go into the
project manifest (`limits:` in `envoryx.yml`) and show up in
`envoryx project show`.

## Health checks

A running container is not a working application: PHP may answer every
request with a 500, the database may be unreachable, a deploy may have left
the app in maintenance mode. A project's **Overview** section sets up a health
check: a path such as `/health` or Laravel's `/up` that must answer with the
expected status.

- Envoryx asks the web server the way the proxy does: directly on the project
  network, with the project's host name and `X-Forwarded-Proto: https`, so
  the application sees a normal visitor. DNS and certificates play no part -
  the check is about the application. Redirects are not followed; `/health`
  answering `302 → /login` is a failure that names the target.
- Every 30 seconds by default (10 s to 1 h), 5 seconds per answer, and the
  application is **down** after 3 failures in a row. Down and back each send
  one notification (`project.down`, on by default); the project shows a
  warning while it is down. *Test* runs the check once, also before saving.
- The check pauses while the project is stopped, while an operation (start,
  deploy, restart …) runs on it and while its application container is not
  running, since that case is already reported as a stopped project. Stopping a
  project that is down ends the outage without a "back" message.
- The path should check what the application needs (database, cache, queue)
  and answer quickly. It is part of `envoryx.yml` (`healthcheck:`) and shown
  by `envoryx project show`.

## Resource history

Envoryx records what every running project container uses, once a minute:
CPU (in cores), memory, network traffic and disk I/O (both per second).
Once an hour it measures each project's disk space: its volumes (database,
caches, search, object storage), its project directory and its backups.

- A project's **Resources** section draws all of it over the last hour up to a
  year: CPU and memory per container, network and disk I/O for the project,
  disk space by kind. Every chart has a table view.
- The **dashboard** lists every project with its average and peak CPU and
  memory, its disk space and a CPU sparkline, busiest first: the quick way
  to find the project that eats the server.
- Values are kept at full detail for a day, then as 5-minute averages (peaks
  are kept) for a week, then as hourly averages. *Settings → Retention →
  Resource history* sets how long (7, 30, 90 days, which is the default, or a year)
  and deletes the recorded values. A year of hourly values for a project with
  five containers is about 60,000 rows in the Envoryx database; a few MB.
- Disk I/O is what the kernel attributes to the container: writes still in
  the page cache show up when they are flushed. Stopped projects record
  nothing; their gap stays open in the chart.

## Stopping and restarting

Project operations (creating, starting, restarting, updating, deleting a
project, project backups and restores) run to completion on the server even
when the browser tab that started them is closed, the page is reloaded or the
connection drops. The UI shows the result on the next load.

When Envoryx itself is stopped (`docker stop`, an Unraid update, an instance
restore) it refuses new project operations, gives the running ones
`ENVORYX_SHUTDOWN_GRACE` (default 8 s) to finish and then abandons what is
left. An abandoned operation is recorded on the project as *interrupted by an
Envoryx restart*; the project keeps its previous state and the action can be
run again after the restart (a start or restart picks up where it left off, a
creation is rolled back). Backups in flight are discarded and retried at the
next scheduled run.

The grace period must stay below the container's stop timeout, otherwise
Docker kills the process first: Docker's default is 10 s; the compose file
sets `stop_grace_period: 90s` with `ENVORYX_SHUTDOWN_GRACE: 80s` so an image
pull can usually finish. On Unraid the stop timeout is global (*Settings →
Docker → Docker stop timeout*, default 10 s), so raise it and the template's
*Shutdown grace* together if you want the same behaviour.

### Orphaned resources

A container, network or volume with Envoryx labels whose project no longer
exists in the database (after restoring an older instance backup, a wiped
`/config`, or a delete that Docker only half completed) is an *orphan*. The
reconciler stops and removes orphaned containers and networks on its own once
it has seen them in two consecutive passes (about a minute), so they do not
linger in the Docker list of the host; a network another container still
uses is left alone. Volumes hold data and are never removed automatically:
they stay listed on the *Docker* page with a *Remove* button. Volumes are
named after the project slug, so a project created again under the same name
picks its data up. Every removal shows up on the dashboard (*Envoryx acted
on its own*), as a notification (`docker.orphans_removed`, on by default)
and in the audit log.

### Projects and the Envoryx container

The project containers are independent of the Envoryx container: they carry
Docker's `unless-stopped` restart policy and keep running while Envoryx is
stopped or updated. For maintenance windows, *Settings → General → Projects
and the Envoryx container* ties them together:

- Stopping the Envoryx container (`docker stop`, Unraid stop, host shutdown)
  stops every running project after the grace period above, all projects in
  parallel, then the shared helpers such as the database browser. The
  projects keep their desired state: this is Envoryx going down, not the
  user stopping them.
- On the next start, Envoryx starts the projects that were running before,
  also after a reboot of the host, where `unless-stopped` alone would leave
  them stopped (Docker does not restart a container that was stopped
  explicitly).
- A restart Envoryx asks for itself (after an instance restore) does not
  bounce the projects.
- The dashboard lists the projects Envoryx started again (*Envoryx acted on
  its own*), a `projects.resumed` notification names them, and each start
  is in the audit log with *Envoryx (automatic)* as the actor.

The stop has to fit into the Envoryx container's stop timeout together with
the grace period: a project stack takes about as long as its slowest
container (databases up to 10 s). The compose file's `stop_grace_period: 90s`
is enough for typical setups; on Unraid raise *Docker stop timeout* to 60 s
or more when the option is on. A project that is not stopped in time stays
running and is picked up on the next start like any other.

## Updating

```
docker compose pull && docker compose up -d
```

Image tags: `:latest` is the newest release, `:<version>` (e.g. `0.1.0`) a
fixed release, `:main` the development branch (every push, may break). What
changed is in [CHANGELOG.md](CHANGELOG.md) and on the GitHub releases page.
Envoryx checks GitHub once a day for a newer release and shows it on the
dashboard and in Settings (`ENVORYX_UPDATE_CHECK=false` turns that off).

Envoryx's state lives in `/config`, `/projects` and `/backups` only. Replacing the image
never touches projects. Database migrations run automatically and are forward
only; a database newer than the binary is refused with a clear error.

Before the first migration of a new version Envoryx writes an **instance
backup** (`pre-migrate-…`) to `<backups dir>/_instance/`. To go back to the
previous version: pull the old image, then restore that backup from *Settings → Backups → Instance backups*. If the old version refuses to start because the schema is
newer, its error message names the pre-migrate backup to restore by hand (see
[Instance backups](#instance-backups)).

## Keeping runtimes up to date

- **Patch releases** (e.g. 8.5.3 → 8.5.4): the `envoryx-php` images are rebuilt
  weekly from the official `php` images. A project **Restart** pulls the tag
  again and recreates the container only if the image actually changed. Until
  then the project keeps running on the previous build; nothing changes
  behind your back.
- **Rolling back**: when a restart replaced containers with a rebuilt image,
  the project's *Overview* shows *image updated <date>* next to the service
  with a **Roll back** button. It recreates the containers from the image
  they ran before (the same for MariaDB, Caddy, …: every image tag Envoryx
  pulls). The previous image is kept out of *Docker → unused images* for as
  long as a project can roll back to it. A rolled-back project stays on that
  image through further restarts (a *previous image* badge marks it) until you
  choose **Use current image**. Rolling back is a per-tag safety net, not a
  version change: for another PHP version use the Runtime section.
- **New minor versions** (e.g. 8.6): a weekly workflow compares
  `internal/runtime/php_versions.json` with endoflife.date and Docker Hub and
  opens a pull request when a version appears, becomes stable or reaches EOL.
  Merging it builds the images and the next Envoryx image shows the version in
  the wizard. Pre-release versions are marked *preview*.

Node.js images (`envoryx-node:*`) follow the same scheme with `node_versions.json`,
Python images (`envoryx-python:*`, official `python:<v>-slim-bookworm` plus uv,
git and build dependencies) with `python_versions.json`, Go images
(`envoryx-go:*`, official `golang:<v>-bookworm` plus air, Delve and gotestsum)
with `go_versions.json`, Ruby images (`envoryx-ruby:*`, official
`ruby:<v>-slim-bookworm` plus build dependencies and the debug gem) with
`ruby_versions.json`, Java images (`envoryx-java:*`, Eclipse Temurin
`<v>-jdk-noble` plus Maven and Gradle, LTS releases only) with
`java_versions.json`, .NET images (`envoryx-dotnet:*`, the official SDK image
`mcr.microsoft.com/dotnet/sdk:<v>.0-noble` plus `dotnet-ef` and netcoredbg, LTS
releases only) with `dotnet_versions.json`. You change a project's Node, Python,
Go, Ruby, Java or .NET version on
the Runtime section, like the PHP version.

## Custom runtime images

A runtime (PHP, Node.js, Python, Go, Ruby, Java, .NET) can run an image of your own
instead of the Envoryx image, for a system package, a PHP extension or a tool the Envoryx
image doesn't have. Open the project's **Runtime** section; the *Runtime images* card has one
row per runtime with three choices:

- **Envoryx image** - the catalogue image of the selected version (the default).
- **Image from a registry** - any reference, like `ghcr.io/acme/php:8.4`. Envoryx pulls it
  when you save (so a moved tag is picked up), and a restart pulls it again like the
  Envoryx images.
- **Dockerfile in the project** - a path relative to the project directory, like
  `.envoryx/php.Dockerfile`. Envoryx builds it and tags the result
  `envoryx-build/<runtime>:<hash>`.

Workers and cron jobs run in their runtime's image, so they follow. Databases, services
(Redis, Mailpit, …) and the web server always keep the vetted images. Git operations and
templates keep using the Envoryx image, too: they need its tools, and a Dockerfile from the
repository doesn't exist before the clone.

**The Dockerfile's directory is the build context**: everything in it is sent to Docker,
so give the Dockerfile a directory of its own (`.envoryx/` is a good place) rather than the
project root. A context with more than 5000 files or 512 MB is refused; symbolic links in
it are left out, and `.dockerignore` is not read. The hash behind the tag covers the
Dockerfile and every file in its directory, so when one of them changes the next start
(or restart) builds a new image, and an unchanged context is never built twice. Projects
and branch environments with the same context share the image. **Rebuild without cache**
builds again with fresh base images and without Docker's build cache (for a newer Envoryx
image, or an `apt-get` that should fetch newer packages). The build uses Docker's classic
builder, so BuildKit-only syntax (`RUN --mount=…`, heredocs) isn't available.

Start from an Envoryx image to keep every feature:

```dockerfile
# .envoryx/php.Dockerfile
FROM ghcr.io/envoryx/envoryx-php:8.4
RUN apt-get update && apt-get install -y --no-install-recommends ghostscript \
    && rm -rf /var/lib/apt/lists/*
COPY conf/ /usr/local/etc/php/conf.d/
```

The end of the last build's output is shown under the row. A build that fails when you
save, rebuild or restart leaves the running containers alone (a restart builds before it
stops anything) and keeps the setting; a start that can't build fails with Docker's
message. Either way you fix the Dockerfile and try again.

**The check.** Every image is checked when it's set and after every build: Envoryx runs
it once with `sh -c` and looks for what it relies on - a shell, `git`, `socat` (waiting for
the database, SSH port forwarding), `ssh`, the runtime itself (`php` and `php-fpm`, `node`
and `npm`, `python3`, `go`, `ruby` and `bundle`, `java`, `dotnet`) and the runtime's tools
(Composer and Xdebug; corepack; pip and uv; air, Delve and gotestsum; rdbg; Maven and
Gradle; netcoredbg and dotnet-ef). An `ENTRYPOINT` other than tini or the official images'
entrypoints is named, as it runs before every command Envoryx starts and has to end with
`exec "$@"`. The findings show as warnings with what doesn't work without them, for
example *socat is missing: no waiting for the database at start and SSH port forwarding*.
Envoryx uses the image anyway. Images built from an Envoryx image carry its
`envoryx.runtime` label; a PHP image without it is told that the extension switches have
no effect.

**Private registries.** Logins go under **Settings → Private registries**: the
registry host (`ghcr.io`, `registry.example.com:5000`, `docker.io` for Docker Hub),
a username and a password or access token (for GHCR a token with `read:packages`).
Envoryx pulls with them and passes them to builds for their `FROM` images. The password is
never shown again; saving without one keeps the stored password. Setting an image that
can't be pulled for lack of a login points you here.

In `envoryx.yml` each runtime takes `image:` or `dockerfile:` (not both):

```yaml
php:
  version: "8.4"
  dockerfile: .envoryx/php.Dockerfile
node:
  version: "24"
  image: ghcr.io/acme/node-tools:24
```

Setting or removing a custom image needs the **admin** role (like changing a runtime);
developers can rebuild. Built images that no container uses any more show up under
*Docker → unused images* with the catalogue images and go with *Remove unused images*;
images from registries you named are never removed by Envoryx.

The API: `PUT /projects/{id}/services/{kind}/image` with `{"image": "…"}` or
`{"dockerfile": "…"}`, `DELETE …/image` (back to the Envoryx image), `POST …/image/build`
(rebuild without cache), and `GET`/`PUT /settings/registries` (admin; passwords are
write-only). The project's services carry `customImage` with the setting, the warnings
and the last build's output.

## Secret key

Envoryx encrypts the secrets it stores (AES-256-GCM):

- in the database: Git tokens, project variables marked as secret, the service
  settings (database, RabbitMQ, Meilisearch/Typesense, object storage and external
  server credentials, addon secrets), the single sign-on client secret and the private
  registry logins;
- `/config/notify.json`, `/config/offsite.json` and `/config/ca/acme.json` (as a whole);
- the project export in each project backup's `backup.json`.

Not encrypted: the private key files that other programs read directly (the local CA,
the SSH host key, the ACME account key, the deploy key in `/config/ssh/`) and
`/config/dbtool/connections.json`, which the database browser reads. User passwords and
API, session and invitation tokens are stored as hashes anyway.

**Where the key comes from.** `ENVORYX_SECRET_KEY` wins: base64 of 32 bytes or 64 hex
digits, e.g. from `openssl rand -base64 32`. Without it Envoryx creates `/config/secret.key`
(mode 0600) at the first start. The variable is the better place: a copy of `/config`
(an appdata backup, a stolen disk image) then holds only encrypted secrets. Switching from
the file to the variable needs nothing else: Envoryx still reads the file at that start,
re-encrypts everything with the variable's key and deletes the file.

*Settings → Secret key* shows the key's ID and where it comes from. *Show key*
reveals the key itself (the reveal is written to the audit log); keep a copy somewhere
outside Envoryx. Instance backups never contain the key, so without a copy an instance
backup can't be restored on another host.

**Replacing the key.**

- With the key file, *Replace the key* creates a new key and re-encrypts every secret,
  the three files and every project backup. The new key waits in `secret.key.next` until
  that is done, so an interruption leaves both keys at hand and the next start finishes
  the job.
- With `ENVORYX_SECRET_KEY`, set the new key there and the current one as
  `ENVORYX_SECRET_KEY_OLD` for one start; Envoryx re-encrypts everything, and the old
  variable can go afterwards.

Either way, instance backups made before keep needing the old key.

**A missing or wrong key.** Every start first checks the key against the database. If it
doesn't fit (the variable was dropped, the key file is gone, a copied `/config` without
its key), Envoryx refuses to start and says which key ID the database needs and how to
give it: as `ENVORYX_SECRET_KEY_OLD` for one start (everything is then re-encrypted with
the current key), as `ENVORYX_SECRET_KEY`, or as `/config/secret.key`. A start that fails
this way leaves no new key file behind. Without the key the secrets are lost; everything
else (projects, files, settings without secrets) is not affected by the key.

**Updating from an earlier version.** The first start of this version creates the key file
(unless `ENVORYX_SECRET_KEY` is set) and encrypts the existing secrets, files and project
backups in place; the log says how many. Take a copy of the key afterwards. Instance
backups made with an earlier version are not rewritten and still contain the secrets
unencrypted; delete them (and their offsite copies) once you have a new one.

## Git deploy key

For SSH repositories Envoryx generates an Ed25519 key pair on first use under
`/config/ssh/`. Copy the public key from **Settings → Git deploy key** (or the
project's Git section) into your repository as a read-only deploy key. Private
HTTPS repositories use an access token per project instead (GitHub:
fine-grained PAT with *Contents: read*; GitLab: username `oauth2` + token).

## Branch environments

A branch environment is a copy of a project on another branch of its repository, with its
own containers, database and URL. Open the parent's **Branches** section, load the branches of
the repository and pick one. Envoryx copies the project the way *Duplicate* does (files
including the ignored `.env`, `vendor/` and `node_modules/`, the database, the bucket,
workers and cron jobs), switches the copy to the branch (`git fetch`, `git checkout -f -B
<branch> origin/<branch>`, `git clean -fd`), starts it and deploys it once. It's called
`Shop (feature/login)` and answers at `shop-feature-login.test`; the identifier is cut to
40 characters and numbered if it's taken. The parent needs a repository and a git checkout
(clone it first), and an environment can't have environments of its own.

**Deploy commands** are set once on the parent, one per line, and run in the environment's
application container as the project owner:

```
composer install
php artisan migrate --force
```

A deploy pulls the branch fast-forward only (local commits in the environment stay), then
runs the commands in order; the first one that fails stops it. The environment's *Branches*
section shows the commit, the outcome and the output, and has *Pull and deploy* and *Run deploy
commands*. The environment has to be running.

With **Watch the repository** on, Envoryx asks the remote with `git ls-remote` every few
minutes (5 unless set otherwise). That works behind NAT and needs no webhook. On each
check:

- an environment whose branch is gone is deleted, files and data included;
- a running environment behind its branch is pulled and deployed (a failed deploy of the
  same commit isn't retried);
- one new branch that matches a pattern (`feature/*`; `*` doesn't match `/`) and isn't the
  parent's own branch gets an environment, up to five unless set otherwise. If creating it
  fails, Envoryx tries again with the branch's next commit, not on every check.

**Stop idle environments after** stops an environment nobody opened for that many days,
counted from the last request through the proxy or the last start. A stopped environment
keeps its files and data. A parent can't be deleted while it has environments; delete
those first. The same settings go into `envoryx.yml` as `branches:` (see *Project
manifest*), and the notifications `branch.failed` and `branch.changed` report failed
creations and deploys, and created, deleted or idle-stopped environments.

The API: `GET /projects/{id}/branches` (settings, environments, last check), `PUT
…/branches/settings`, `GET …/branches/remote` (asks the repository), `POST …/branches`
with `{"branch": "feature/login"}` (admin, like creating a project) and `POST
/projects/{id}/deploy` on an environment (`{"pull": false}` runs only the commands).

## Backups

Project backups (database dump, files, configuration) are created from the
project's **Backups** section and stored as plain directories, one per backup,
under `<backups dir>/<project>/`. Changing a database's version takes a
database backup automatically first (source *upgrade*); if no dump can be
taken because the project is stopped, the upgrade is refused, because the server
rewrites its data directory on the first start and cannot go back.

By default the backups directory is `/config/backups`, i.e. on the appdata
share. Backups are large and rarely read, so you may want them on the array
instead of the cache SSD: mount a host directory at `/backups` (Unraid: add
the optional *Backups* path in the template, e.g. `/mnt/user/backups/envoryx`;
Compose: uncomment the `/backups` volume) and Envoryx uses it automatically.
`ENVORYX_BACKUPS_DIR` overrides the location explicitly. To move existing
backups, stop Envoryx, move the contents of `/config/backups/` into the new
directory and start again. A warning is logged at startup while backups are
left behind in the old location.

Include the backups directory in your regular off-machine backup (e.g. the
Unraid Appdata Backup plugin or an rsync job), use the per-backup download, or
let Envoryx copy the backups offsite itself (see *Offsite backups* below).

**Scheduled backups**: Backups → *Scheduled backups*: daily or weekly at
a given hour (server local time; set `TZ` on the container for your zone),
keep the last N scheduled backups (manual ones are never deleted), optionally
including `vendor/`, `node_modules/` and the framework build caches
(`.next/`, `.nuxt/`, `.output/`), which are skipped by default because they
are large and reproducible. Failures raise a notification.

### Instance backups

*Settings → Backups → Instance backups* snapshots Envoryx itself: the SQLite database
(accounts, sessions, API tokens, projects and their service settings, domains,
workers, settings), the local CA, the SSH host key and deploy keys,
notification settings and the generated per-project configuration. Project
files and Docker volumes are **not** included; that's what project backups
are for. A backup is a single `envoryx-<id>.tar.gz` under
`<backups dir>/_instance/` (`instance.json` with version/schema, `envoryx.db`
as a consistent `VACUUM INTO` copy, `config/…`). The [secret key](#secret-key)
is never in it: `instance.json` only records its ID, and the secrets in the
archive stay encrypted.

- **Create** a backup any time (e.g. before an update); **download** it and
  **import** it on another host to move an installation.
- **Automatic**: before every schema migration (`pre-migrate-…`) and before
  every restore (`pre-restore-…`). The last 5 of each kind are kept; manual and
  imported ones stay until deleted.
- **Restore** needs the confirmation word `restore`. Envoryx records the
  request, restarts in place (the process replaces itself, so no restart policy
  is needed) and applies the backup before opening the database: current
  state → `pre-restore` backup, then database and config are replaced.
  Project files are untouched; the containers and networks of projects that
  were created after the backup no longer belong to a project and are removed
  by the reconciler about a minute later, their volumes appear as orphans in
  *Docker* until you remove them there (see *Orphaned resources*). All
  sessions end; sign in again with the credentials from the backup. The
  restored database starts with an `instance.restored` audit entry naming the
  backup, the `pre-restore` safety copy and who requested it. The audit rows
  written after the backup was taken are gone with the old database.
- A backup from a **newer** Envoryx (higher schema version) is refused; an
  older one is migrated forward on start (with its own `pre-migrate` backup).
- A backup whose secrets are encrypted with **another key** (another host, or a
  key replaced since) asks for that key in the restore dialog; the key must
  match the ID the backup records. After the restart everything is
  re-encrypted with this instance's key. Only the current key counts: after a
  replacement, older backups need the old key, so keep a copy of it as long as
  you may restore one of them.

Manual restore without the UI (e.g. Envoryx doesn't start): stop the
container, unpack the archive (`envoryx.db` to `/config/envoryx.db`, deleting
`envoryx.db-wal`/`-shm` if present, and `config/*` over `/config/`) and start
again. If the backup was made with another key, give that key for this start
as `ENVORYX_SECRET_KEY_OLD` (see [Secret key](#secret-key)).

Still include `/config`, `/projects` and the backups directory in your regular
off-machine backup (e.g. the Unraid Appdata Backup plugin or an rsync job), or
set up an offsite target.

### Offsite backups

*Settings → Backups → Offsite backups* copies backups to storage outside the
host. The local backups stay the working copies; each target keeps its own.

| Type | For | What it needs |
|------|-----|---------------|
| S3-compatible | AWS S3, Backblaze B2, Wasabi, Hetzner Object Storage, Cloudflare R2, MinIO | endpoint URL, bucket, region, access key and secret key (path-style addressing) |
| SFTP | Hetzner Storage Box (port 23), a NAS, any SSH server | host, port, user, password and/or an OpenSSH private key without passphrase |
| WebDAV | Nextcloud/ownCloud (`https://cloud.example.com/remote.php/dav/files/<user>`, app password), Storage Box, NAS | URL, user, password |

*Test* writes, lists, reads and deletes a small file under the target's
folder. For SFTP it also records the server's key (SHA256 fingerprint); a
different key later stops the uploads until the pin is cleared in the target.
Credentials live in `/config/offsite.json` (mode 0600, encrypted with the
[secret key](#secret-key)) and are never sent back to the browser.

**What goes up.** Per target:

- *Scheduled project backups*: every scheduled backup of every project is
  copied once it is taken. *Keep per project* rotates the scheduled copies on
  the target (0 = keep all); copies made by hand are never rotated away.
- *Daily instance backup*: at the chosen hour Envoryx takes an instance
  backup (kind `scheduled`, the last 5 stay locally) and copies it. This is
  what a fresh Envoryx needs after the host is gone.
- Everything else on request: *Copy offsite* next to a backup (project or
  instance), *Also copy offsite* when creating one, `envoryx backup create
  --offsite` or `envoryx backup offsite <project> <backup>`.

Uploads run in the background. A failed one is retried after 5, 15 and 45
minutes and 2 hours, then stays failed (the button tries again); every
failure raises the *Backup failed* notification. The Backups section shows each
copy's state next to the backup.

**Encryption** is a per-target switch and on by default. Archives are
encrypted with [age](https://age-encryption.org) and a scrypt passphrase
before they leave the host, so the provider only sees `.age` files; backups
carry the project's passwords, tokens and data. Keep the passphrase somewhere
else: without it nothing can be restored, and after losing the host a fresh
Envoryx needs it. The files also open without Envoryx:
`age -d -o backup.tar backup.tar.age`.

**Layout** on a target, below its folder (default `envoryx`, so several
instances can share one bucket with different folders):

```
projects/<slug>/<backup>.<contents>.<source>.tar[.age]   a project backup (the download format)
instance/<instance backup id>.tar.gz[.age]              an instance backup
```

**Getting a backup back.** The *Offsite copies* card in a project's Backups
section lists what the target holds for the project (also backups that were
deleted here), and *Fetch* puts one back into the local list, from where it is
restored as usual (`envoryx backup remote|fetch <project>` on the command
line).

**After losing the host** (disaster recovery):

1. Start a fresh Envoryx and create an account.
2. *Settings → Backups*: add the same target with the same folder and
   passphrase.
3. *Instance backups → From an offsite target*: fetch the newest one and
   restore it. Envoryx restarts with the old accounts, projects, settings and
   targets; sign in with the old credentials.
4. For each project: *Backups → Offsite copies → Fetch*, then restore
   database, files and bucket. Starting the project recreates its containers.

## Several databases

A project isn't limited to one database. Next to the first one (the
*primary*, reached as host `database` with `DB_*` and `DATABASE_URL`) it can
have any number of additional databases, each with a name of its own:
PostgreSQL for reporting next to MariaDB, or a second MariaDB in another
version for a legacy part of the application. Add them in the wizard
(*Project & services → Additional databases*) or later in the Database section
(*Add database*); the section switches between the databases of the project.

An additional database named `analytics`:

| | |
|---|---|
| Container | `envoryx-<project>-db-analytics` |
| Volume | `envoryx-<project>-db-analytics` |
| Host in the project network | `analytics` |
| Variables | `ANALYTICS_DB_CONNECTION`, `ANALYTICS_DB_HOST`, `ANALYTICS_DB_PORT`, `ANALYTICS_DB_DATABASE`, `ANALYTICS_DB_USERNAME`, `ANALYTICS_DB_PASSWORD`, `ANALYTICS_DATABASE_URL`, `ANALYTICS_DB_SERVER_VERSION` (MariaDB, MySQL and PostgreSQL) or `ANALYTICS_MONGODB_URI` (MongoDB) |

Names are 1-24 lowercase letters, digits and dashes starting with a letter
(dashes become underscores in the variables: `legacy-db` → `LEGACY_DB_DB_HOST`);
names another container of the project answers to (`database`, `redis`,
`web`, the engine names …) are refused. Each database has its own generated
credentials, its own published port if wanted, its own version and upgrades,
and everything the Database section offers works per database: credentials,
password rotation, databases on the server, Adminer, snapshots, cloning.

- **Backups** dump every database of the project: the primary to
  `database.sql.gz` as before, an additional one to
  `database-<name>.sql.gz`. Restoring puts back every dump whose database the
  project still has and names the ones it skipped.
- **Snapshots** are taken of one database; the list and *Restore* go by it,
  and the ten newest are kept per database.
- **Cloning** copies any database of the same engine into the selected one,
  of another project (`staging`'s `analytics` into `local`'s `analytics`), or
  another database of the same project.
- **Duplicating and renaming** carry the additional databases along: the
  copy gets their contents and ports of its own, a rename moves their names
  and logins like the primary's.
- `envoryx.yml` lists them under `databases:` (see *Project manifest*); the
  CLI takes `--add-database NAME=TYPE[:VERSION]` on `project create` and
  `--db NAME` on every `db` command (`db clone` also `--source-db`, with
  `primary` for the source's primary). The API addresses one with `?db=<name>`
  on the `/database…` routes, lists all with `GET /projects/{id}/databases`
  and adds, changes or removes them with `PATCH /projects/{id}` and
  `"databases": {"analytics": {"enabled": true, "type": "postgresql"}}`
  (`"enabled": false, "removeData": true` removes one with its volume).

## External databases and Redis

Instead of a container of its own, a database (the primary or an additional
one) or Redis can be a server that already runs elsewhere: the MariaDB on your
Unraid server, a PostgreSQL in the company network, a managed database. Choose
*On an external server* in the wizard, when adding a database in the Database
section, or when adding Redis in the Services section, and enter host, port, user,
password and database. *Test connection* tries it right away; Envoryx tests it
again before it stores it and says what failed (wrong password, database not
visible to the user, host unreachable).

- **Supported:** MariaDB, MySQL and PostgreSQL, and Redis (with or without a
  password). The version you pick selects the client tools Envoryx uses for
  backups and the connection, so choose the server's major version (for
  PostgreSQL the client must not be older than the server).
- **A server on the Docker host itself** is reached as
  `host.docker.internal`; Envoryx adds that name to every container of the
  project. `localhost` is refused: inside a container it is the container.
- **The application** gets the same variables as with a container (`DB_HOST`,
  `DB_PORT`, `DATABASE_URL` … or `ANALYTICS_DB_*`, `REDIS_HOST`, `REDIS_PORT`,
  `REDIS_URL` and `REDIS_PASSWORD`), pointing at the server; special characters
  in the password are escaped in the URLs.
- **What works:** backups, snapshots and restores (a restore overwrites the
  database on the server, with the usual confirmation), cloning, the site
  import, Adminer, listing databases and creating new ones.
- **What Envoryx leaves alone:** it never drops a database on the server,
  never changes the password (change it on the server, then under *Edit
  connection*), publishes no port, and removing the database or Redis from the
  project only forgets the connection. Renaming the project keeps the
  server's database and user names.
- **Duplicating** a project gives the copy local containers instead: the
  database is filled with the external one's data, Redis starts empty. The
  copy can never change the external server.
- **`envoryx.yml`** records the connection without the password
  (`database: {type: mariadb, external: {host: …, port: …, username: …,
  database: …}}`, `redis: {external: {host: …}}`). Applying a manifest keeps
  an existing connection and changes it with the stored password; a new
  external connection is skipped, and creating a project from a manifest that
  has one is refused; set it up in Envoryx.

## Running tests

The *Tests* section of a project lists the test suites Envoryx finds in the
project directory and runs them in the runtime container as the project owner,
with the output live like an action:

| Suite | Found by | Filter |
|---|---|---|
| Pest | `vendor/bin/pest` | `--filter` |
| PHPUnit | `vendor/bin/phpunit`, or `bin/phpunit` (Symfony's bridge) | `--filter` |
| npm scripts | `test` and `test:*` in `package.json` (yarn or pnpm when their lock file is there) | passed on to the script |
| Playwright | `playwright.config.*` | `--grep` |
| Cypress | `cypress.config.*` | `--spec` |
| pytest | `pytest.ini`, `conftest.py`, `[tool.pytest]`, pytest in the requirements | `-k` |
| Django | `manage.py` | test label |

A suite that `composer.json` asks for but that is not installed yet is listed
with the hint to run `composer install`. The filter is handed to the runner as
one argument, never through a shell, and cannot start with a dash.

PHPUnit, Pest, Playwright, Cypress and pytest write a JUnit report, which
Envoryx reads after the run: the counts and every failed test with its
message, file and line and the full text of the failure. For npm scripts and
Django the exit code decides. The last 50 runs of a project are kept with
their result and the end of their output (*Recent runs*); cancelling a run or
closing the tab stops it and records it as cancelled. While tests run, the
project is busy like during an action.

Playwright and Cypress need their browsers and the system libraries those
use; the Envoryx Node image does not include them, so browser tests usually
belong on a machine or CI runner that has them.

The API: `GET /projects/{id}/tests` (suites and recent runs),
`GET /projects/{id}/test-runs/{run}` (one run with its output) and the
WebSocket `GET /projects/{id}/tests/{suite}/ws?filter=…` (`operate` scope),
which sends `{"type":"result","run":…}` before it closes.

## Sharing a project

*Share* in a running project's header puts it on a temporary public https
address, so you can show a client or a colleague work in progress without a
VPN, port forwarding or an account anywhere. Envoryx starts a Cloudflare quick
tunnel (`cloudflare/cloudflared`) next to the project, pointed at the proxy
(so the project's *Rules* apply: a password, headers, redirects), or straight
at the application when the proxy has no plain HTTP listener, and shows the
address it gets:
`https://<random-words>.trycloudflare.com`. The address is random and changes
with every share.

A share lasts 5 minutes to 24 hours (an hour unless chosen otherwise; the
dialog offers 15 minutes to 24 hours) and
ends early when the project stops, when it is renamed or deleted, or with
*End the share*. Recreating the application for new variables keeps it. Only
outgoing connections are needed: the host has to reach Cloudflare on port
7844.

**Anyone who has the address reaches the project from the internet, without
signing in**, unless the project's rules ask for a user name and password,
which is the way to protect a share. Share a staging copy, not data that has to
be protected, and keep in mind that an application which builds absolute links from
`APP_URL`/`ENVORYX_URL` still points them at the local address. Starting a
share needs an `admin` token or user, ending one `operate`; both land in the
audit log.

```sh
envoryx project share shop --for 2h     # prints the public address
envoryx project share shop --status
envoryx project share shop --stop
```

The API: `GET /projects/{id}/share`, `POST /projects/{id}/share`
(`{"minutes":60}`) and `DELETE /projects/{id}/share`.

## Package cache

Composer, npm, Yarn, pip, uv, Go (modules and build cache), Bundler, Maven, Gradle and NuGet keep their
downloads in one cache that every project shares: `/config/cache`, mounted at
`/var/cache/envoryx` into the PHP, Node, Python, Go, Ruby, Java and .NET containers, the
workers and the one-shot containers that scaffold a template. A package is
downloaded once, whichever project asks for it next, so the second Laravel
project is created in a fraction of the time of the first. The variables that
point the tools there (`COMPOSER_CACHE_DIR`, `npm_config_cache`,
`YARN_CACHE_FOLDER`, `PIP_CACHE_DIR`, `UV_CACHE_DIR`, `GOMODCACHE`, `GOCACHE`,
`BUNDLE_USER_CACHE`, `NUGET_PACKAGES`, `NUGET_HTTP_CACHE_PATH`) can be
overridden per project like any other. pnpm keeps its store in the project
home.

The cache only grows. *Settings → Package cache* shows what each tool
keeps there and empties one tool's part or all of it; the next install
downloads again. Instance backups leave it out. On Unraid the cache lives with
`/config` on the appdata share, usually on the SSD pool.

## Local LLMs (Ollama)

*Services → Ollama* (or `--ollama` / `--ollama-gpu` with `envoryx project create`)
adds an Ollama server to a project. The application reaches it at
`http://ollama:11434`, injected as `OLLAMA_HOST`, `OLLAMA_BASE_URL` and `OLLAMA_URL`
- the names the Ollama libraries, LangChain and Laravel Prism read.

Models are downloaded on the Ollama card: type a name from the
[Ollama library](https://ollama.com/library) (`llama3.2`, `qwen3:8b`,
`nomic-embed-text`, `hf.co/<org>/<repo>:<quant>` …) and watch it arrive. A download
continues when you leave the page and can be cancelled; the next download of the same
model picks up where it stopped. The project has to be running.

All projects share one model store, `/config/ollama` (on Unraid
`/mnt/user/appdata/envoryx/ollama`): a model is downloaded once, and deleting it
removes it for every project (the dialog says so). Models are several GB each, so
keep an eye on the share. Removing Ollama from a project leaves the store alone; to
free the space, delete the models first or empty the directory.

**GPU.** *Use the GPU* hands the host's NVIDIA GPUs to the container
(`docker run --gpus all`). Docker needs the NVIDIA Container Toolkit for it; on
Unraid that's the *Nvidia Driver* plugin from Community Applications, then a restart of
Docker. Envoryx starts a short test container before it switches the GPU on and
refuses with that hint when Docker cannot hand the GPU over, so Ollama keeps running
on the CPU. Without a GPU, small models (up to about 4B parameters) answer at usable
speed on a current CPU; larger ones need the GPU or patience.

## Database browser (Adminer)

*Settings → Database browser* switches on an in-browser database tool for
projects with MariaDB, MySQL or PostgreSQL (MongoDB is not supported by the
Adminer image; use the published port with Compass). Nothing runs until the
first click on **Open database** in a project's Database section: Envoryx then
pulls `adminer:5`, starts one shared container `envoryx-dbtool` on its own
network, joins it to the project's network and opens Adminer in a new tab,
already logged in as the project user (the root user is available too by
changing the user name in the URL).

How it stays private: Adminer is served under the Envoryx UI at `/dbtool/`,
so the normal Envoryx session is required and no extra port, host name or
certificate is involved. The credentials are written to
`/config/dbtool/connections.json` (mode 0640, owned by `PUID`, mounted
read-only into the container, which runs as `PUID:PGID`; never sent to the
browser) and refreshed on every open, so rotated passwords and
new projects are picked up without a restart. Switching the browser off
removes the container, its network and the credentials file. The container
is not touched by *unused image* pruning while enabled.

## Object storage (S3)

Projects can get S3-compatible object storage (the **Services** section, or
*Object storage* in the wizard). Envoryx runs one RustFS container per project
with a persistent volume, generates an access/secret key pair, creates a bucket
named after the project at start-up and injects two sets of variables into the
application containers:

| Generic            | Laravel / AWS SDK            | Value                                   |
|--------------------|------------------------------|-----------------------------------------|
| `S3_ENDPOINT`      | `AWS_ENDPOINT`               | `http://s3:9000` (inside the project)   |
| `S3_REGION`        | `AWS_DEFAULT_REGION`         | `us-east-1`                             |
| `S3_BUCKET`        | `AWS_BUCKET`                 | the project slug                        |
| `S3_ACCESS_KEY`    | `AWS_ACCESS_KEY_ID`          | generated                               |
| `S3_SECRET_KEY`    | `AWS_SECRET_ACCESS_KEY`      | generated                               |
| `S3_USE_PATH_STYLE`| `AWS_USE_PATH_STYLE_ENDPOINT`| `true`                                  |
| `S3_PUBLIC_URL`    | `AWS_URL`                    | `https://<project>-s3.<base>/<bucket>`  |
| `S3_PUBLIC_ENDPOINT` | -                          | `https://<project>-s3.<base>`           |

A Laravel `s3` disk works without further configuration
(`FILESYSTEM_DISK=s3`); code written for any S3-compatible provider runs
unchanged once it reads endpoint, keys and bucket from these variables and
uses path-style addressing.

**Reaching the storage from the browser.** The embedded proxy serves the S3
API as `<project>-s3.<base domain>` (covered by the same wildcard DNS and
certificate as the project), so `Storage::url()` links, public assets and
direct uploads work in the browser. Presigned URLs meant for a browser must
be signed against that public endpoint (`S3_PUBLIC_ENDPOINT`): the signature
covers the host name, and a URL signed for `s3:9000` is useless outside the
project network. The S3 API and the web console are also published on host
ports (shown in the Services section) for local tools such as `aws s3 --endpoint-url`.

**Public read.** Real providers honour `public-read` ACLs; RustFS accepts but
ignores them. Envoryx therefore puts a bucket policy on the bucket that lets
anyone read every object, switched on by default so `Storage::url()` behaves
as it would in production. Turn *Anyone may read objects* off in the Services
section to test that nothing relies on it; then only presigned URLs and
authenticated requests work.

The console (RustFS's own UI) opens from the Services section; sign in with the
project's access keys. Removing the object storage deletes the bucket volume
and needs the bucket name as confirmation.

**Backups.** Project backups can include the bucket (on by default for manual
and scheduled backups when the project has object storage): every object is
stored as a plain file in `storage.tar.gz`, named by its key, with the content
type kept as a `user.mime_type` extended attribute. That's readable with any
tar and independent of the server's on-disk format. Restoring uploads the objects
again, optionally emptying the bucket first; the storage container must be
running for both.

## Addons

An addon adds a service Envoryx doesn't have built in, without a new Envoryx version: a
YAML file describes one container per project - its image and versions, environment,
data volumes, a port with an optional web UI, a health check, the variables it hands your
application and the credentials worth showing. Redis, Mailpit, the search engines, Ollama
and object storage stay built in, with their extra features; addons sit next to them.

**Installing.** Addon files live under `/config/addons/<name>.yml`. Manage them under
**Settings → Addons** (admins only):

- **New addon** opens an editor with a starting file; **Edit** changes an installed one.
- **Examples** ships five files to install as they are or to adjust first:
  - *pgAdmin* (PostgreSQL web UI in desktop mode, no login; the card shows the project
    database's host, user and password to add it as a server);
  - *phpMyAdmin* (signed in to the project's MySQL or MariaDB database);
  - *Elasticsearch* 9 or 8 (one node without security, port publishable,
    `ELASTICSEARCH_URL`/`_HOST`/`_PORT`);
  - *Soketi* (Pusher-compatible WebSocket server for Laravel Echo and broadcasting,
    reached by browsers through its web UI address, `PUSHER_*`);
  - *Keycloak* 26 (development mode, admin password generated per project,
    `KEYCLOAK_URL`), handy for trying single sign-on.
- **Install from a URL** downloads a file over http(s), a raw GitHub link for example.

Every file is checked when it's saved: unknown keys, a name Envoryx uses itself, an image
that isn't a reference, a placeholder the addon can't fill - each is refused with the
reason. A file under `/config/addons` that doesn't parse is listed with its error. A file
can be at most 64 KB.

**What an addon can't do.** The format has no field for privileged mode, capabilities,
host directories, the host network or devices, and unknown keys are errors, so an addon
always runs with named volumes on its project's network, like the built-in services.

**The file.**

```yaml
name: widget                  # 2-30 lower-case letters, digits and dashes; container
                              # envoryx-<slug>-addon-widget, not a name Envoryx uses itself
title: Widget                 # shown in the UI (defaults to the name)
description: What it's for.
homepage: https://example.com
versions:                     # at least one; the default (or the first) when none is chosen
  - version: "2"
    image: acme/widget:2
    default: true
  - version: "1"
    image: acme/widget:1
hostname: widget              # name on the project network (defaults to the name)
port: 8080                    # what it listens on: {{port}}, web UI, host port
webUI: true                   # https://<slug>-widget.<base domain> through the proxy
publishPort: true             # offer "Publish the port on the host" (desktop clients)
command: ["serve", "--name", "{{project.slug}}"]   # replaces the image's command
user: "1000:1000"             # inside the container
env:                          # the container's environment
  WIDGET_PASSWORD: "{{secret.password}}"
  WIDGET_DB: "{{database.host}}:{{database.port}}"
  discovery.type: single-node # images that read settings from the environment may use dots
secrets: [password]           # 24 random characters per project: {{secret.password}}
volumes:
  - {name: data, path: /data}
  - {name: cache, path: /cache, noBackup: true}
healthcheck:
  test: ["CMD-SHELL", "wget -qO- http://127.0.0.1:8080/health || exit 1"]
  interval: 10s               # also timeout, startPeriod (durations up to 1h), retries
inject:                       # handed to the application containers
  WIDGET_URL: "http://{{host}}:{{port}}"
credentials:                  # shown on the project's card
  - {label: Password, value: "{{secret.password}}", secret: true}
```

The placeholders every template can use: `{{host}}` and `{{port}}` (the addon inside the
project network), `{{url}}` (the web UI's address, empty without `webUI`),
`{{project.slug}}`, `{{project.name}}`, `{{project.url}}`, `{{database.type}}`,
`{{database.host}}`, `{{database.port}}`, `{{database.name}}`, `{{database.user}}`,
`{{database.password}}` (the primary database; empty when the project has none) and
`{{secret.<name>}}` for every declared secret. Names under `env:` may contain lower-case
letters and dots; names under `inject:` are regular variable names (`[A-Z_][A-Z0-9_]*`).
The injected variables never replace Envoryx's own (an addon cannot change `DB_HOST`),
and the project's variables always win over them. A secret credential is masked until
revealed, and viewers don't get its value at all.

**In a project.** Installed addons show up at the bottom of the project's **Services**
section: *Add <title>* with a version and, when the addon offers it, *Publish the port on the
host*. Developers can add, change and remove them; installing files takes an admin. Adding
one creates the container and, if the addon injects variables, recreates the application
containers with them. The card shows the web UI's address, the internal host and port, the
host port, the injected variables, the volumes, the image and the credentials. An addon
with a web UI always gets a host port too, like Mailpit.

**Changing a file.** A project keeps a copy of the definition it runs, with its generated
secrets. Saving a new version of an installed file updates that copy in every project
using it (new secrets are generated, existing ones kept; a version the file no longer
lists becomes the default), and the containers follow at the next start or restart - the
addon's container, and the application containers when the injected variables changed.
A file still used by a project can't be deleted (Envoryx names the projects); a project
whose file is gone keeps running its copy but can't change it. Removing an addon from a
project deletes its container and, after a confirmation, its volumes.

**Backups.** A database backup (manual, scheduled, before an upgrade) also archives every
addon volume without `noBackup` as `addon-<name>-<volume>.tar.gz`. The addon's container is
stopped while its volumes are read, so the archive is consistent, and started again
afterwards; `busybox:1.37` does the reading (pulled once). Restoring a backup's database
part empties those volumes and unpacks the archives, for the addons the project still has.
Snapshots of a single database leave the addon volumes out. A copy of a project gets the
same addons with secrets of its own, new host ports and empty volumes; renaming moves the
volumes along.

In `envoryx.yml`, installed addons go under `addons:`; the server must have the file:

```yaml
addons:
  phpmyadmin: true                         # the default version
  elasticsearch: {version: "8", exposePort: true}
```

An addon the file no longer names is removed with `--prune`, together with its volumes.

The API: `GET /addons` (installed files with the projects using them, plus the examples),
`POST /addons` with `{"source": "<yaml>"}` or `{"url": "https://…"}`, `GET /addons/{name}`
(with the file) and `DELETE /addons/{name}`, all admin; `GET /projects/{id}/addons` (the
project's addons and the installed ones it can add) and `PUT /projects/{id}/addons/{name}`
with `{"enabled": true, "version": "8", "exposePort": true}` or
`{"enabled": false, "removeData": true}` (developer). The audit log records
`addon.installed` and `addon.removed`; changes in a project are `project.updated`.

## Logs

The Logs section has two views per container. *Live* follows the output as it
comes. *History* searches the past output: a time range (a preset or
from/to), a text search and a level filter, a chart of lines, warnings and
errors over time (a click on a bar zooms into that slot) and the most
frequent errors and warnings, grouped so that the same message with other
numbers, ids or times counts as one. *Download* saves every line that matches,
as text or, through the API with `format=jsonl`, as JSON lines.

Containers do not report a level, so Envoryx reads it from the text: words
such as *Fatal error*, `[error]`, `ERROR:`, *Exception* or *Traceback*, a
`level` field in JSON or logfmt lines, and a 5xx status in access logs count
as errors; *Warning*, `[warn]` and *Deprecated* as warnings. stderr alone does
not make a line an error.

The same filters work from scripts: `envoryx project logs shop --since 6h
--level error`, `--grep`, `--until`, and `-o FILE` to save everything that
matches (`--since` takes a duration back from now such as `30m`, `6h`, `7d`
or an RFC 3339 time).

### Log history

Docker keeps a container's output only as long as the container exists (and
at most 30 MB of it): a project that is updated, reconfigured or renamed gets
new containers and starts with empty logs. So Envoryx copies the output of
every project container into `/config/logs/<project>/<service>/<day>.jsonl` as
it is written, and the History view reads from there, across restarts and
recreated containers. The footer says which source a result came from.

- Output written while Envoryx was stopped is picked up when it comes back,
  as long as Docker still has it; nothing is stored twice.
- Days are UTC. Finished days are compressed (gzip), days older than the
  retention (default 7) are deleted, and when the history grows beyond its
  limit (default 1 GB for all projects together) the oldest days go first.
  Both are set in *Settings → Retention → Log history*, which also shows the
  space in use.
- Deleting a project deletes its history. *Delete stored logs* empties the
  whole history; lines Docker still holds are not collected again.
- The history is not part of instance backups.
- Switched off, nothing new is stored and the Logs section reads the containers
  again, as before.

## Workers (queues, schedulers)

Under *Workers & cron* you add long-running processes from a preset list: Laravel
`schedule:work`, `queue:work`/`queue:listen` (queue names), Horizon,
Reverb, Symfony `messenger:consume` (transports) and Scheduler, a PHP script
or a composer script (PHP image); npm scripts and Node scripts (Node
image); Python scripts and modules, Django management commands, Celery
worker and beat (Python image); Go programs of the module (Go image); Solid
Queue, GoodJob, Sidekiq, rake tasks and Ruby scripts (Ruby image, after the
bundle install); jars and Maven goals or Gradle tasks (Java image); .NET
projects and DLLs (.NET image). Every worker is its own container
(`envoryx-<project>-worker-<name>`) from the image of the runtime its
preset names, runs as `PUID:PGID` with the project's environment (and
php.ini for PHP, the venv `PATH` for Python), restarts automatically
(Docker `unless-stopped`) and follows start/stop/restart of the project.
*Workers & cron* offers only the presets whose runtime the project has.
`queue:work` stops after an hour (`--max-time`) so code changes
are picked up on the automatic restart; use `queue:listen` for instant
reloads. Logs are in the Logs section; up to 10 workers per project.

## IDE integration (PhpStorm, WebStorm, VS Code)

Every project has an **IDE** section with all values ready to copy.

**Remote interpreter over SSH.** Envoryx runs an SSH server on port 2222
(publish it, or use the container's own IP on `br0`). User name = project
slug (`shop`): it lands in the project's application container, PHP when
the project has PHP, else Python, else Go, else Ruby, else Java, else .NET, else Node.
Projects with several runtimes also accept `shop.php`, `shop.python`, `shop.go`,
`shop.ruby`, `shop.java`, `shop.dotnet` and `shop.node` to pick one explicitly (the IDE section lists these rows only
then). Password = one of your API tokens (*API tokens & MCP*), or sign in with a public key
you stored under *Settings → SSH keys* (both in *My account*, for everyone). Tokens and keys act with your roles, so they only open the projects you may work
in. The *Admin keys* under *Settings → SSH access* open every project, like an
admin. Each session is a `docker exec` into that
container as the project owner; there is no shell
on the host. SFTP exposes `/var/www/html` (the project) and `/home/envoryx`
(a persistent home for tool caches and IDE helpers).

- PhpStorm: *Settings → PHP → CLI Interpreter → … → From Docker, Vagrant,
  VM, WSL, Remote… → SSH*; PHP path `/usr/local/bin/php`, helpers path
  `/home/envoryx/.phpstorm_helpers`, path mapping *project folder* →
  `/var/www/html`. Afterwards PHPUnit/Pest, Composer and Artisan run inside
  the container from the IDE.
- WebStorm (Node-only project, or `shop.node` next to PHP): *Settings →
  Languages & Frameworks → Node.js → Node interpreter → Add… → SSH*, host
  and port from the IDE section, user `shop`, Node path `/usr/local/bin/node`,
  project path `/var/www/html`. npm scripts, the test runner and the
  debugger then run in the container.
- PyCharm (Python project, or `shop.python` next to PHP): *Settings →
  Project → Python Interpreter → Add Interpreter → On SSH…*, host and port
  from the IDE section, user `shop`, interpreter `/var/www/html/.venv/bin/python`,
  project path `/var/www/html`. pytest, manage.py and pip then run in the
  container.
- VS Code: Remote-SSH works the same way (`ssh -p 2222 shop@<host>`); open
  `/var/www/html` as the remote folder.

A static project (no PHP, Python, Go, Ruby, Java, .NET or Node) has no application container, so
SSH sessions are refused for it.

The project must be running for sessions to open. Commands are logged to
the audit log (`ssh.exec`), failed logins are rate limited per IP.

### JetBrains Gateway (optional)

Gateway runs the complete IDE backend on the server and connects a thin
client. In Envoryx this is opt-in per project (IDE → *Allow JetBrains
Gateway*): it enables SSH port forwarding into the container and mounts a
shared backend cache (`/config/jetbrains`, ~1.5 GB per IDE version,
downloaded once). The backend runs as the project owner inside the
application container (PHP, else Python, else Go, else Ruby, else Java, else .NET, else
Node; user `<slug>`, and `<slug>.python` / `<slug>.go` / `<slug>.ruby` / `<slug>.java` /
`<slug>.dotnet` / `<slug>.node`
pick one next to PHP) and needs 2-4 GB RAM plus CPU while indexing. Nothing
runs until you connect. Envoryx ships no JetBrains
software: Gateway itself is free, the IDE backend is uploaded by your
Gateway client and licensed through it. Whoever connects needs a valid
subscription for that IDE (PhpStorm, WebStorm or All Products Pack), the
server needs nothing. Gateway → *SSH → New connection*
with the values from the IDE section, choose PhpStorm/WebStorm, project
directory `/var/www/html`. Close the project in Gateway or use *Stop IDE
backend* to free the memory. Small NAS boxes: leave it off.

The tunnel to the backend needs `socat` in the runtime image. The PHP and Node
images have it since September 2026; the Python, Go, Ruby, Java and .NET images had it from
the start. Troubleshooting:

- *Host unreachable* right after installing the backend: use *Restart* on
  the project (a restart pulls the runtime images and recreates the
  container; plain stop/start does not). `ssh forward … "via":"network"` in
  the Envoryx log means the container still runs an image without socat.
- Gateway hangs or shows the host as unreachable although `ssh` works:
  set `ENVORYX_LOG_LEVEL=debug` and follow `docker logs Envoryx | grep '"ssh'`.
  Every command Gateway runs is logged with exit code and the first bytes
  of output, every tunnel with the listener it was relayed to and the
  bytes transferred.
- Changing the runtime (PHP version, Xdebug, …) recreates the container and
  ends the IDE backend; close the project in Gateway first.

## Xdebug

Runtime → PHP → **Xdebug**: enables step debugging for that project
(port 9003, mode `debug,develop`, `start_with_request=yes`). Xdebug connects
back to the machine that made the request (behind Envoryx's proxy that
address comes from `X-Forwarded-For`) and falls back to the *developer
machine* set in Settings → Domains & HTTPS → Project links & developer machine (or a per-project override). Map
`/var/www/html` to your project folder in the IDE; the IDE section shows the exact
PhpStorm/VS Code settings. Turn it off when you are done: it slows PHP down.

## Notifications

Settings → **Notifications**: pick a channel (ntfy, Discord or Slack
webhook, Telegram bot, e-mail via SMTP, or a generic JSON webhook), choose
the events and send a test. Events: a project that should be running is
stopped/broken (and when it recovers), an application fails its health check
and when it answers again (see *Health checks*), project creation failed, a
container ran out of memory (see *Resource limits*), backup failed, Let's
Encrypt renewal failed/succeeded, a branch environment could not be created or
deployed, or was created, deleted with its branch or stopped for being idle (see
*Branch environments*), Envoryx started, Envoryx failed (it refused
to start because of a corrupt database, a network filesystem or a failed
migration, or a background task crashed and was restarted). Repeats are
throttled (unhealthy project once per 6 h, failed renewal once per day). The
failed-start notification is sent before the process exits and needs no
database, only the channel configured in `/config/notify.json`.
Secrets live in `/config/notify.json` (0600, encrypted with the
[secret key](#secret-key)). SMTP authentication requires
STARTTLS or TLS. An event type added by an update follows its default until
the event selection is saved again, so a new alarm is not silently off for
anyone who once chose their events.

## Users and roles

The setup page creates the first admin. Everyone else comes in through an invitation:
*Settings → Users → Invite user*, a name and a role, and Envoryx shows a link
(`https://<envoryx>/invite/<token>`) that works once within 48 hours. Envoryx sends no mail,
so you pass the link on yourself; it's shown only this once. Whoever opens it sets a
password (at least 10 characters) and is signed in. The same button on an existing user
(*Reset password*) hands out a new link for a forgotten password, and using it signs that
user out everywhere else.

| Role | May |
|------|-----|
| Viewer | look: projects, status, logs, statistics, backups (the `read` scope) |
| Developer | work with projects: start, stop, restart, terminal, actions, git, backups, databases, SSH/SFTP (`operate`) |
| Admin | everything: creating and deleting projects, settings, TLS, instance backups, users (`admin`) |
| No access | nothing, except in the projects the user has a role of their own in |

*Projects* on a user gives them another role in particular projects, which replaces the
global role there: a viewer everywhere can be a developer in one shop, or a developer have
no access to the production copy. An admin stays admin in every project. A project a user
has no access to is out of sight: it's missing from the lists and the dashboard, MCP
doesn't find it, and the API and SSH refuse it. Creating projects, copies and
branch environments takes an admin of the whole instance; a branch environment starts with
the project roles of its parent.

Users who aren't admins see a smaller interface: *Settings* has only *My account*
(profile with appearance and password, API tokens & MCP, SSH keys), the Docker page and *New project* are
gone, viewers can't start or stop projects and don't get the *Terminal* and *Actions* sections,
and *History* and the *Docker plan* are for admins. The server checks every request on its own,
so hiding is a courtesy, not the protection.

A user can be disabled, which ends their sessions and stops their API tokens and SSH keys
until they're enabled again, or deleted with their tokens and project roles. Nobody can
disable or delete themselves, and the last admin who can still sign in can't be demoted,
disabled or deleted. The audit log records `user.invited`, `user.joined`, `user.updated` and
`user.deleted`.

The API for scripts that know what they're doing: `GET/POST /api/v1/users`,
`PATCH/DELETE /api/v1/users/{id}` (`role`, `disabled`), `POST /api/v1/users/{id}/invite`
(a new link) and `PUT /api/v1/users/{id}/projects/{project}` with `{"role": "developer"}`
(`""` removes it). They need an admin's browser session; API tokens can't manage users.
The command line deliberately has no user management.

### Single sign-on (OpenID Connect)

*Settings → Users → Single sign-on* signs people in through Authentik, Keycloak,
Authelia, Google or any other OpenID Connect provider. Register Envoryx there as a
confidential client with the redirect URL the card shows
(`https://<envoryx>/api/v1/auth/oidc/callback`, on the address you reach Envoryx at), then
fill in the issuer URL, client ID and secret and a label for the button, try *Test* and
switch on *Offer single sign-on on the login page*. The login page then has a *Sign in with
…* button next to the password form. The client secret is stored and never shown again;
leave the field empty to keep it.

Envoryx asks for `openid profile email groups` and uses the authorization code flow with
PKCE, state and nonce; the ID token is checked against the provider's keys. The username
comes from the `preferred_username` claim (or the one you set); providers that don't send
it (Google, Dex) fall back to the part of the e-mail address before the `@`, then to the
name, with characters a username can't have turned into dots. The groups come from the
`groups` claim, from the userinfo endpoint when the ID token has none (Keycloak needs a
*Group Membership* mapper for that).

*Roles from groups* maps group names to roles; the first match from admin down wins, and
it's applied at every sign-in, so the provider stays the source of truth (it never takes
away the last admin, though). *Users in none of these groups* decides what happens to
everyone else: they may not sign in (the default), sign in without access to any project,
as viewers or as developers. Without any group mapping the role stays whatever an admin set
in Envoryx. Project roles are always set in Envoryx.

An account is linked by the provider's subject. With *Create users at their first sign-in*
a new person gets an Envoryx user right away; without it only invited users get in, and the
invitation links the account at the first single sign-on while the link is still valid.
An existing Envoryx account is never taken over by its name: to connect one, give it a new
link (*Reset password*) and sign in through the provider before the link expires. When
something goes wrong, the login page says why.

The settings API: `GET/PUT /api/v1/settings/oidc` (admin; the answer has `hasSecret`
instead of the secret) and `POST /api/v1/settings/oidc/test`. The public
`GET /api/v1/auth/oidc` tells the login page whether to show the button.

## AI assistants (MCP)

Envoryx ships an MCP server at `/mcp` (streamable HTTP). Create a token under
**Settings → API tokens & MCP** (under *My account*, for every user); the page shows a ready-to-paste client
configuration:

```json
{
  "mcpServers": {
    "envoryx": {
      "type": "http",
      "url": "https://envoryx.test/mcp",
      "headers": { "Authorization": "Bearer stq_…" }
    }
  }
}
```

Claude Code: `claude mcp add --transport http envoryx https://envoryx.test/mcp --header "Authorization: Bearer stq_…"`.
Use `http://<host>:8787/mcp` if the proxy/HTTPS is not set up.

Available tools: list/get projects, list runtimes, create project (PHP
version + extensions, database, Redis, Memcached, Mailpit, RabbitMQ,
Meilisearch, Typesense, OpenSearch, Ollama, object storage, Node, Python, Go,
Ruby, Java, .NET, git clone, env),
start/stop/restart, get logs, list/run actions (composer, artisan, npm …),
list/create databases, list/create backups, add domain. Deleting projects,
dropping databases and restoring backups are intentionally not exposed -
do those in the UI. Example prompt: *"Create a Laravel project called
test-api with PHP 8.4, MariaDB and Redis, then run composer install."*

Projects without PHP: pass `phpVersion: "none"` plus `nodeVersion`,
`nodeDevServer: true` and `nodePreset` (`vite`, `next`, `nuxt`, `generic`;
optional `nodeScript`, `nodePort`, `nodePackageManager`), or a Node template
(`vite`, `next`, `nuxt`) which fills the dev-server defaults. The result
carries `serves` (`php`, `python`, `go`, `ruby`, `java`, `dotnet`, `node` or `static`),
`devUrl` and a `directUrl` that points at the node host port while the dev
server serves the project.
Example prompt: *"Create a Node.js project called dashboard from the Nuxt
template, no PHP, then show me its logs."*

Python projects: pass `phpVersion: "none"` plus `pythonVersion`,
`pythonServer: true` and `pythonPreset` (`django`, `flask`, `asgi`, `wsgi`,
`module`; optional `pythonApp`, `pythonPort`, `pythonMode`), or a Python
template (`django`, `flask`, `fastapi`) which fills the server defaults.
`serves` is `python` then and `directUrl` points at the Python host port.
Example prompt: *"Create a FastAPI project called inventory-api with
PostgreSQL, no PHP, then run pip install."*

Go projects: pass `phpVersion: "none"` and `goVersion`, plus `goServer: true`
(optional `goPackage`, `goPort`, `goMode`) or a Go template (`go`, `gin`,
`echo`), which switches the server on. `serves` is `go` then.

Ruby projects: pass `phpVersion: "none"` and `rubyVersion`, plus `rubyServer:
true` (optional `rubyPreset` `rails`/`rack`, `rubyPort`, `rubyMode`) or a Ruby
template (`rails`, `rails-api`, `sinatra`), which switches the server on.
`serves` is `ruby` then.

Java projects: pass `phpVersion: "none"` and `javaVersion`, plus `javaServer:
true` (optional `javaPreset` `spring-boot`/`quarkus`/`jar`, `javaPort`,
`javaMode`) or a Java template (`spring-boot`, `quarkus`), which switches the
server on. `serves` is `java` then.

.NET projects: pass `phpVersion: "none"` and `dotnetVersion`, plus
`dotnetServer: true` (optional `dotnetPreset` `aspnetcore`/`dll`,
`dotnetProject`, `dotnetPort`, `dotnetMode`) or a .NET template
(`aspnet-webapi`, `aspnet-mvc`, `blazor`, `razor-pages`), which switches the
server on. `serves` is `dotnet` then.

Branch environments: `list_branch_environments`, `create_branch_environment`
(`project`, `branch`; admin) and `deploy_branch_environment` (`project`, optional
`pull: false`) answer with the branch, the deployed commit and the deploy's status and
output.

### Scripting the REST API

The same tokens authenticate the REST API (`/api/v1/...`) for scripts, CI
jobs and the [command line](#command-line); send them as
`Authorization: Bearer stq_…`. Bearer requests need neither a session cookie
nor the browser CSRF headers:

```sh
curl -H "Authorization: Bearer stq_…" https://envoryx.test/api/v1/projects
curl -H "Authorization: Bearer stq_…" -X POST https://envoryx.test/api/v1/projects/<id>/restart
```

### Token scopes

Every token has a scope, chosen when it is created; each level includes the
ones below it:

| Scope     | Allows                                                                                                                                                              |
|-----------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `read`    | Looking: project list and details, status, logs, statistics, backups list, runtimes. No secrets (no database credentials, no deploy key), no changes.               |
| `operate` | Working with existing projects: start/stop/restart, image rollback, actions (composer, artisan …), creating backups and databases, git, domains, SSH/SFTP, terminal, database browser, adding installed addons. Default for new tokens. |
| `admin`   | Everything an admin may do: creating and deleting projects, settings, TLS, notifications, instance backups, image clean-up, restores, dropping databases, installing addons. |

A token can additionally be **limited to particular projects**. It then sees
only those in listings, every other project answers `403` (REST) or "no project
matches" (MCP), instance-wide endpoints (dashboard, settings, Docker overview)
are closed, and it can't create projects, whatever its scope. Use this
for an assistant that should work on one project only.

A token belongs to the user who created it and never does more than that user may, whatever
its scope: an `admin` token of a developer can't delete projects, and a token loses what its
owner loses. A scope the owner can't reach anywhere is refused when the token is created.
Everyone sees and revokes their own tokens; admins see all of them with their owner.

Refusals carry the reason (`this token has read scope, the operation needs
operate`) so scripts and assistants can tell what kind of token they need.
`GET /api/v1/auth/me` shows the calling token's name, scope and projects.

No token can change the password, create/revoke tokens or manage users; those
need a browser session. Audit entries record `user (token: name)`. A request that
presents an invalid or revoked token is rejected even if a valid session
cookie is also sent. Tokens created before scopes existed keep full access
(`admin`, all projects).

## Command line

The Envoryx binary is also its own client. `envoryx project …`,
`envoryx backup …`, `envoryx db …`, `envoryx git …` and `envoryx import` talk to
a running server over the REST API with an API token, which makes them equally
at home in an SSH session, a cron job or a CI pipeline. On the host the
container's own binary does the job:

```sh
docker exec -it envoryx envoryx project list
docker exec -it envoryx envoryx project exec shop -- php artisan migrate --force
```

Inside the container the address is known (`ENVORYX_LISTEN`); only the token is
needed. From a workstation or a build agent, name the server once:

```sh
envoryx login --url https://envoryx.example.com      # asks for the token, then stores it
envoryx login --url https://envoryx.example.com --token "$ENVORYX_TOKEN"
```

`login` checks the token against the server before writing
`~/.config/envoryx/cli.json` (mode 0600; `ENVORYX_CLI_CONFIG` points somewhere
else). `--token -` reads it from stdin, which is what a provisioning script
wants. `envoryx whoami` shows whose token it is and what it may do, and
`envoryx logout` forgets it again (the token itself is revoked under
*API tokens & MCP* in the settings). Without a stored configuration, `ENVORYX_URL`
and `ENVORYX_TOKEN` work just as well, which is handy in CI, where nothing should be
written to disk.

### What it can do

```sh
envoryx project list                                  # name, slug, state, URL
envoryx project show shop                             # services, versions, ports, git
envoryx project create "Shop" --php 8.4 --database mariadb --template laravel --start
envoryx project duplicate shop "Shop Test"            # config, files and database
envoryx import ./old-site "Old Site" --db dump.sql    # an existing website as a project
envoryx project rename shop "Acme Blog" --yes         # identifier, URL and data follow
envoryx project start|stop|restart shop
envoryx project delete shop --yes [--delete-files]
envoryx project logs shop --service php --tail 200 --follow
envoryx project exec shop -- composer install         # any command, any container
envoryx project run shop                              # list the catalogue actions
envoryx project run shop artisan:migrate              # run one, with live output
envoryx backup list|create|restore|download|delete shop
envoryx db snapshot shop --note "before a migration"  # the database alone
envoryx db snapshots shop                             # what there is to go back to
envoryx db restore shop <snapshot> --yes              # put one back
envoryx db clone local --from staging --yes           # staging's data into local
envoryx db snapshot shop --db analytics               # an additional database (all db commands)
envoryx project create "Shop" --database mariadb --add-database analytics=postgres
envoryx project share shop --for 2h                  # a temporary public address
envoryx git status|pull shop                          # and: git checkout shop main
envoryx project branches shop                         # its branch environments
envoryx project branch shop feature/login             # a copy of shop on that branch
envoryx project deploy shop-feature-login [--no-pull] # pull and run the deploy commands
```

A project is named by its name, its slug or its id. `--json` hands the API's
own answer to `jq` instead of a table; `--service` picks a container other
than the project's application container (`php`, `python`, `go`, `ruby`,
`java`, `dotnet`, `node`, `web`, `database`, `redis`, `memcached`, `mailpit`, `rabbitmq`,
`meilisearch`, `typesense`, `opensearch`, `opensearch-dashboards`, `ollama`,
`storage`, `worker:<id>`). `envoryx project create --from-json file.json`
sends a create request the flags don't cover (everything the wizard offers),
and flags given alongside it win.

Every command exits `0` on success, `1` on failure and `2` on a usage error.
`envoryx project exec` passes the command's own exit code on, so
`envoryx project exec shop -- php artisan migrate --force || rollback` does
what it looks like. Its stdout and stderr stay apart, input is piped in
(`envoryx project exec shop -- sh -c "cat > /tmp/x" < file`, up to 512 KiB),
and the command runs as the project owner in the project directory, the same
place the browser terminal starts in. For an interactive shell or a large
pipe, use SSH: `ssh shop@<host> -p 2222` (see *IDE integration*).

What the token may do, the CLI may do: a `read` token lists and follows logs, an
`operate` token also starts, stops and runs commands, and creating or deleting
projects needs `admin`. Refusals arrive as the API wrote them (`this token has
read scope, the operation needs operate`), and every command lands in the audit
log with the token's name.

### Project manifest (envoryx.yml)

`envoryx.yml` describes a project in its repository: runtimes, services,
domains, environment, workers and cron jobs. Commit it with the code, and a
fresh clone is one command away from the same environment:

```sh
git clone git@github.com:acme/shop.git && cd shop
envoryx up                        # creates "shop" on the server, or brings it in line
envoryx up --dry-run              # only show what would change
envoryx project manifest shop -o envoryx.yml   # write the file for an existing project
```

`envoryx up` looks for the file in the current directory and its parents (up to
the repository root). When the server has no project of that name yet, it
clones the repository itself (the `origin` remote and the checked-out branch,
over the same credentials as the wizard: deploy key for SSH URLs,
`--git-token` for private HTTPS), creates the project exactly as described and
starts it. The local checkout only supplies the file and the repository URL; a
remote that exists only on this machine (a path, `file://`) is refused. When
the project exists, `up` compares it with the file, applies the differences in
one restart and starts it (`--no-start` leaves it stopped). The project is
found by `name:` from the file, `--name`, or the directory name.

```yaml
version: 1
name: shop
docroot: public
web: {server: nginx}             # caddy (default), apache, nginx
php:
  version: "8.4"
  extensions: [bcmath, intl, pdo_mysql, redis, zip]
  memoryLimit: 512M              # also uploadMaxFilesize, postMaxSize, maxExecutionTime,
  xdebug: true                   # displayErrors, errorReporting, xdebugMode, xdebugIdeKey
database: {type: mariadb, version: "11.4", exposePort: true}
databases:                       # additional databases, reached by their name
  analytics: {type: postgres}    # host analytics, ANALYTICS_DB_* variables
redis: true                      # or {version: "8", exposePort: true}
mailpit: true                    # also memcached, rabbitmq, meilisearch, typesense,
opensearch: {dashboards: true}   # opensearch, storage: {publicRead: false}
ollama: {gpu: true}              # models are not part of the manifest
# database: {type: mariadb, external: {host: host.docker.internal, port: 3306,
#            username: shop, database: shop}}   # never the password
domains: [api.shop.example.com]
env:
  APP_ENV: local
secrets: [STRIPE_SECRET]         # names only - values never go into the repository
workers:
  - {name: queue, preset: "laravel:queue", arg: default}
cron:
  - {name: prune, schedule: "0 3 * * *", command: php artisan model:prune, timeout: 5m}
limits:                          # per container; see "Resource limits"
  app: {cpus: 2, memory: 2G}
  services: {memory: 1G}
  pids: 4096
healthcheck:                     # see "Health checks"; or just: healthcheck: /health
  path: /health
  status: 200                    # defaults: 200, every 30s, 5s timeout, down after 3
  interval: 1m
branches:                        # see "Branch environments"
  watch: true
  patterns: ["feature/*"]
  idleStopDays: 7                # also: pollMinutes, maxEnvironments
  deploy: [composer install, php artisan migrate --force]
```

`node:`, `python:`, `go:`, `ruby:`, `java:` and `dotnet:` take the fields of the wizard (`devServer`, `preset`,
`port`, `script` …; `server`, `preset`, `app`, `debug` …; `server`, `mode`,
`package`, `port`, `debug`, `debugPort`; `server`, `mode`, `preset`, `port`,
`debug`, `debugPort`; the same plus `jar`; `server`, `mode`, `preset`,
`project`, `dll`, `port`). Every runtime also takes `image:` or `dockerfile:` (see
*Custom runtime images*); installed addons go under `addons:` (see *Addons*). A setting left out
means Envoryx's default, so the file is the whole desired state: `web:` missing
means Caddy, `extensions:` missing the default set. Unknown keys are errors -
a typo never silently drops a service. The export pins every version, which is
what makes `up` reproduce a project exactly.

Some things deliberately stay out of the file: host ports (the server assigns
them), database, RabbitMQ, search and storage credentials (generated per
project), the repository itself, backup schedules and the Xdebug client host.
Secret values are asked for in a terminal (`envoryx up` prompts for each one
the project does not have yet, `--secret KEY=VALUE` passes one), otherwise the
variable is created empty and named in the output; afterwards the project keeps
its value.

Nothing is removed without `--prune`: a service, variable, domain, worker or
cron job the file no longer has is listed as *kept*. With `--prune` it goes -
for a database or a service with a volume (Redis, RabbitMQ, the search engines,
storage, an addon with volumes) together with its data. Another database `type` counts as such a
removal. A database version lower than the project's is never applied, as the
data format does not go back.

In the web interface the *Git* section shows the project as `envoryx.yml` (copy,
download, save into the project directory) and compares the file in the project
directory with the project. After a `git pull` that brought a changed
manifest, *Apply to project* takes it over. The wizard applies the manifest of
a repository it clones (*Use the repository's envoryx.yml*, on by default); the
file then wins over the services picked in the wizard. The API behind it:
`GET /projects/{id}/manifest`, `PUT …/manifest/file`, `POST …/manifest/plan`
and `…/manifest/apply` (`yaml` in the body, or the file in the project
directory), `POST /projects/from-manifest`. Creating and applying need an
`admin` token, like creating and changing a project.

### The certificate

A server behind the local CA is not trusted by a fresh workstation. Pass the
authority once and store it:

```sh
envoryx login --url https://envoryx.example.com --ca-cert ~/envoryx-ca.crt
```

The file is the one from *Settings → Domains & HTTPS → Download envoryx-ca.crt*
(`ENVORYX_CA_CERT` does the same). `--insecure` skips verification for a quick
look on a trusted network; with a Let's Encrypt certificate neither is needed.

## Audit log

*Settings → Audit log* lists who did what and when: sign-ins, every change to
a project, backups, database operations, commands run through the API or a
terminal, settings. Filter by text (action, user, project, IP, details), by
user (an account's API tokens are included), by category and by date;
*Load older entries* pages back, and a click on a user shows only their
entries. Every project has the same list for itself under *History*.

Open an entry to see everything it recorded. A project change lists each
setting it changed with its value before and after (PHP version, services,
document root, variables, workers …). Secret values never appear, only that a
secret was added or removed.

*CSV* and *JSON Lines* export exactly the entries the filters show (the API:
`GET /api/v1/audit/export?format=csv&action=project.&since=2026-09-01`, with
the same parameters as `GET /api/v1/audit`: `q`, `user`, `action` (a prefix,
repeatable), `project`, `since`, `until`, `after`, `limit`). Cells a
spreadsheet would run as a formula are prefixed with `'`.

*Retention* keeps the log for 30 days up to two years, or forever (the
default); older entries are deleted every hour.

## Lost access

A user who forgot their password gets a new link from an admin (*Settings → Users → Reset
password*, see *Users and roles*). When no admin can sign in any more, the credentials can
be reset from a shell inside the running container; nothing else is touched and no restart
is needed:

```sh
docker exec -it envoryx envoryx admin users            # which accounts exist
docker exec -it envoryx envoryx admin reset-password   # new password, printed once
docker exec -it envoryx envoryx admin reset-password --user stefan --password 'my new password'
docker exec -it envoryx envoryx admin logout-all       # end every browser session
docker exec -it envoryx envoryx admin revoke-tokens    # delete every API token (MCP, SSH/SFTP, scripts)
docker exec -it envoryx envoryx admin reset --yes      # remove all accounts → the setup page returns
```

`reset-password` picks the only account when there is just one and ends its
sessions; `reset` deletes accounts, sessions and API tokens but leaves
projects, settings and backups alone. Open the web interface afterwards and
create the administrator account again. Every command is written to the audit
log with the user `cli`. On Unraid the container's console (*Docker →
Envoryx → Console*) is the same shell.

Failed sign-in attempts are rate-limited per address for up to
15 minutes; if you tried a few wrong passwords just before the reset, wait a
moment.

## Health check

`GET /api/v1/health` returns `{"status":"ok","docker":true,"database":true,…}`
without authentication. The image ships a `HEALTHCHECK` that calls
`envoryx healthcheck`.

The check is a *liveness* check: it fails (503, `"status":"unavailable"`)
only when the database is unusable. An unreachable Docker engine is reported
as `"docker":false` with `"status":"degraded"` but still answers 200 -
Envoryx keeps running and retries the engine on demand, and restarting the
Envoryx container would not fix a Docker problem. Tools that restart
unhealthy containers (autoheal) therefore do not loop on it.
