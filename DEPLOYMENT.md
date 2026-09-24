# Deployment

## Requirements

> **Renamed from Staqio.** Envoryx is not a drop-in upgrade for a Staqio
> installation: the image, config directory (`/mnt/user/appdata/envoryx`),
> `ENVORYX_*` variables, Docker labels, container/volume names and the
> `envoryx.test` base domain all changed. Install Envoryx fresh and recreate
> projects (project files on disk are untouched; use the Staqio backups to restore
> databases).


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
range, e.g. `http://<host>:20000`, and – with the proxy ports mapped and DNS
set up (see [Domains and HTTPS](#domains-and-https)) – as
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
| `ENVORYX_PUBLIC_HOST` | browser address | Host/IP used for project links (see below); also editable in Settings |
| `ENVORYX_PROXY_HTTP` | `:80` | Listen address of the embedded proxy inside the container; empty disables it |
| `ENVORYX_PROXY_HTTPS` | `:443` | HTTPS listener of the proxy (local CA); empty disables HTTPS |
| `ENVORYX_SSH` | `:2222` | Embedded SSH server for IDE remote interpreters; empty disables it |
| `ENVORYX_ADMIN_USER` / `ENVORYX_ADMIN_PASSWORD` | – | Create the first admin non-interactively |
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
  Locks are unreliable there and the database corrupts silently – the classic
  "SQLite is unreliable" story is almost always this. Use a local directory;
  `ENVORYX_ALLOW_NETWORK_FS=true` overrides the check at your own risk.
- **FUSE** (Unraid's `/mnt/user/...` user shares): Envoryx starts and shows a
  warning in *Settings*. It works in practice, but the pool path is the safer
  choice for a database: use `/mnt/cache/appdata/envoryx` (or your pool's
  name) as the host path for `/config`, or enable *Exclusive access* for the
  `appdata` share (Unraid ≥ 6.12, share on a single pool) – then `/mnt/user`
  bypasses FUSE and the warning disappears.
- Free space on `/config`, `/projects` and `/backups` is shown on the
  dashboard and checked every five minutes; below 2 GiB or 5 % a
  `storage.low` notification goes out (once, and again when it recovers). A
  project or instance backup is refused when it would not leave at least
  512 MiB free – a full appdata disk takes the database down with it.
- `/config/logs` holds the log history (see [Logs](#logs)): 7 days and at
  most 1 GB by default, adjustable in *Settings → General → Log history*.
- The database file is integrity-checked at every start (`PRAGMA
  integrity_check`). A damaged file is refused with the name of the newest
  instance backup to restore instead of being migrated or served.

## Host paths (important)

Project containers mount your project files with Docker **bind mounts**. Bind
mount sources are resolved by the Docker daemon **on the host**, not inside the
Envoryx container. Envoryx therefore needs to know that `/projects` inside its
container is `/mnt/user/development` on the host.

Envoryx detects this automatically by inspecting its own container's mounts. If
detection fails (the dashboard shows a red banner and project creation is
disabled), set the two variables explicitly:

```
ENVORYX_PROJECTS_HOST_PATH=/mnt/user/development
ENVORYX_CONFIG_HOST_PATH=/mnt/user/appdata/envoryx
```

On Unraid always use `/mnt/user/...` (or `/mnt/cache/...`) paths – the same
ones you used in the volume mappings.

## Diagnostics

**Settings → Diagnostics** runs a set-up check and lists every finding with
what it looked at, what it found and how to fix it: Docker engine, where
`/config` lives, host paths, disk space, backup directory, database integrity,
host for project links, proxy ports, wildcard DNS, SSH, HTTPS, version,
project/Docker consistency and notifications. Wildcard DNS is checked twice:
from your browser (the check that matters – it fetches
`envoryx-diagnostics-probe.<base domain>` through the proxy, which also proves
the CA is trusted over HTTPS) and from inside the Envoryx container, whose DNS
server is often a different one (the router instead of your ad blocker); that
second result is only a note.
Findings with a button are fixed in place (for example setting the Docker host
for project links); the others link to the setting or to this guide. The
dashboard shows a banner while warnings or errors exist. The same data is
available as `GET /api/v1/system/diagnostics` (admin scope).

## Project links and the Envoryx container's own IP

Project web servers – and Mailpit, database ports, the object storage API and
console – publish their ports on the **Docker host** (the Unraid IP). Envoryx
builds links to them from the address in your browser's address bar. If you
reach Envoryx under a different address – the container has its own IP on
`br0`/macvlan, or you use a reverse proxy – set the host to use for project
links in **Settings → Project links** (or `ENVORYX_PUBLIC_HOST`), typically
the Unraid IP.

When Envoryx detects that it has an IP of its own and no host is set, the
dashboard, the project tabs with such links and the settings show a notice
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
template under another name – say `envoryx.xml` – sorts before `my-Envoryx.xml`
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
**Docker → Add Container → Template repositories** – Unraid then reads
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
| Path `/backups` | optional, e.g. `/mnt/user/backups/envoryx` – keeps backups off the cache/appdata share |
| Path `/var/run/docker.sock` | `/var/run/docker.sock` |
| Variable `PUID` | `99` |
| Variable `PGID` | `100` |

Project ports (20000–20999 by default) are published by the project containers
themselves, not by the Envoryx container – nothing else to map in the template.
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
ones – Envoryx reads its own port bindings and adjusts links accordingly).
When neither port is published, Settings → *Domains & HTTPS* shows a warning
and project links keep using the direct port.

**Own IP (Unraid `br0`, macvlan/ipvlan) or host networking:** there is no
port mapping – the proxy is reachable directly on the container's address
(`http(s)://<envoryx-ip>`). Envoryx detects this and shows the address in the
settings. Your DNS entries for `*.test` must then point at the **Envoryx IP**,
not at the Unraid IP (which is where the direct project ports live).

### Names

- Base domain, default `test` (Settings → Domains & HTTPS). Every project is
  `<slug>.<base>` (`shop.test`), the UI is `envoryx.<base>`.
- Additional names per project in its **Domains** tab (`shop.local`,
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
  on the public internet. `.local` collides with mDNS on macOS/Linux – prefer
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

1. Cloudflare (currently the supported provider): *My Profile → API Tokens →
   Create Token → template "Edit zone DNS"*, restricted to the zone. The
   token needs *Zone:Read* and *Zone:DNS:Edit*.
2. Settings → Domains & HTTPS → **Let's Encrypt**: provider, domain
   (e.g. `dev.example.com` – the wildcard `*.dev.example.com` is added),
   contact e-mail, token, "Use as base domain" → Enable.
3. In your **local** DNS (AdGuard/Pi-hole/…) rewrite `*.dev.example.com` →
   Envoryx's address. Do not create a public record for it.

Envoryx requests the certificate in the background (1–2 minutes, status is
shown in the card), stores it under `/config/ca/custom.*` and renews it 30
days before expiry. The local CA stays as fallback for other names (`.test`).
The staging checkbox uses Let's Encrypt's staging environment to test the
setup without rate limits (certificates from staging are not trusted).

Note: Let's Encrypt publishes every issued certificate in public
certificate-transparency logs, so the existence of `*.dev.example.com` is
visible there – nothing else.

**Own certificate**: alternatively upload any wildcard certificate with its
key in the same card. It is used for every name it covers; other names keep
using the local CA. Certificate and key are stored under `/config/ca/custom.*`
with owner-only permissions and are never returned by the API.

The proxy keeps the original `Host`, sets `X-Forwarded-For/-Proto/-Host` and
supports WebSockets. Projects can therefore generate correct absolute URLs
(`APP_URL=https://shop.test`).

### Node dev servers

Enable "Run a dev server" on the Node.js service (wizard or Runtime tab):
the script (default `dev`) runs as the container's main process and is
reachable at `https://<project>-dev.<base>` through the proxy (HMR
WebSockets included) and on a direct host port. Presets pass host/port to
Vite (`--host 0.0.0.0 --port`, default 5173), Next.js (`-H -p`, 3000) or
Nuxt (`--host --port`, 3000); "Other" only sets `HOST`/`PORT`. Run
`npm install` via Actions first – a crashing script is restarted by Docker
until it works. For Laravel + Vite set `VITE_DEV_SERVER_URL`/`APP_URL`
accordingly, or let Vite's `server.hmr` config point at the dev host name.

#### Node-only projects (Vite, Next.js, Nuxt)

PHP is optional. Pick **Node.js application** on the first wizard step (or
`phpVersion: "none"` over MCP/REST) and a Vite, Next.js or Nuxt template –
or a blank directory / git clone – and the dev server *is* the project:

- **What answers where.** `https://<project>.<base>`, every extra domain and
  `https://<project>-dev.<base>` all reach the dev server
  (`envoryx-<project>-node:<port>`) through the proxy, HMR included. The
  node container's own host port (Domains tab → *Direct access*, MCP
  `directUrl`) works without DNS or the proxy.
- **The HTTP port of the web container is not published** while the dev
  server serves the project. The web container still exists (every project
  has one), but the document root defaults to the project root for Node
  projects and would otherwise expose `.env`, sources and `node_modules`
  statically on the LAN. Turn the dev server off (Runtime tab) and the port
  is published again with the same number.
- **Static build mode.** With the dev server off, the web server serves the
  document root statically – set it to the build output (`dist` for Vite,
  the template does this; `out` for a Next.js `output: "export"` build) and
  run `npm run build` via Actions or the terminal. Enable **SPA fallback to
  index.html** (Web server card) so client-side routes survive a reload;
  without it unknown paths return 404. Static configs deny dotfiles
  (`/.env`, `/.git/…`) on all three web servers.
- **Production build mode.** The dev server has a mode switch (Runtime
  tab): *Dev server* (the default, HMR) or *Production build*. In
  production mode every container start runs the build script (default
  `build`) and then the serve script (`start`; `preview` for Vite) as the
  main process with `NODE_ENV=production` – a production-like run of a
  Next.js/Nuxt SSR app or Vite's preview server, still behind the same
  URLs. Only the serve process gets `NODE_ENV=production`; the container
  itself stays on `development`, so `npm install` from the terminal keeps
  installing devDependencies. A restart rebuilds, so the first response
  after a start takes as long as the build.
- **Debugging.** *Publish the Node.js inspector port* (Runtime tab, dev
  server required) publishes the inspector port (default 9229) on a host
  port of its own; the IDE tab shows host, port, path mapping and
  `package.json` examples. Envoryx does not set `NODE_OPTIONS=--inspect`
  on the container on purpose: npm (a Node process itself) would grab the
  port and the debugger would attach to npm instead of your app. Start the
  inspector in your script – `NODE_OPTIONS='--inspect=0.0.0.0:9229' next
  dev`, `node --inspect=0.0.0.0:9229 node_modules/vite/bin/vite.js` – and
  attach WebStorm (*Attach to Node.js/Chrome*) or VS Code (`request:
  attach`) to the host port. Next.js opens the inspector of its server
  process one port higher (9230); publish that port when debugging server
  code. The inspector executes arbitrary code and is published on all
  host interfaces like the dev-server ports – enable it on trusted
  networks only and turn it off when you are done.
- **Cold start.** A freshly created blank Node project has no
  `package.json` yet: the node container waits for it (log line
  `envoryx: waiting for package.json …`) instead of crash-looping, and the
  proxy shows its "web server did not respond" page until the dev server
  listens – the same page you see during a cold compile after a restart
  (the proxy waits up to 5 minutes for the first response). Templates
  scaffold and `npm install` during creation, so they are ready when the
  project turns green.
- **Vite host check.** Envoryx passes
  `__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS=.<base>` to the container – a
  single leading-dot entry that Vite suffix-matches, so `<project>.<base>`,
  `<project>-dev.<base>` and every extra domain under the base domain pass
  and foreign hosts get 403 (Vite 6.1–8.x; Vite before 8.3 reads the
  variable as one host, which is why it is never a comma-separated list).
  Domains *outside* the base domain must be added to `server.allowedHosts`
  in `vite.config` – the Domains tab reminds you. Next.js and Nuxt have no
  host check.
- **Workers** run in the runtime of their preset: the Laravel/Symfony/PHP
  presets in the PHP container's image, the *npm script* (`npm run <name>`)
  and *Node.js script* (`node <file>`) presets in the Node image with the
  project home mounted. The Workers tab offers only the presets whose
  runtime the project has. Actions offer npm/pnpm/yarn and `node -v`; git
  clone/pull run in a one-shot container from the Node image.
- **Cron jobs** (Cron tab) run any command on a schedule in the PHP, Python
  or Node.js container – as the project owner in the project directory,
  through `sh -c`, with the project's environment. Pick a schedule (every few
  minutes, hourly, daily, weekly, monthly) or type a cron expression; the form
  shows the next runs. Schedules are read in Envoryx's time zone – set `TZ` on
  the container. Jobs only run while the project is running, a run that is
  still going is not started twice, the timeout (default 10 minutes) stops
  the command, and the last 20 runs keep their output. For Laravel, either a
  *Scheduler* worker (`schedule:work`) or a cron job running `php artisan
  schedule:run` every minute – not both.
- **Adding or removing PHP later.** The Runtime tab's PHP card has an
  *Enable PHP* switch on every project. Adding PHP to a Node or static
  project starts a PHP-FPM container, switches the web server to FastCGI
  and makes PHP the application (the project URL leaves the dev server;
  the SPA fallback is dropped). Removing PHP takes the PHP container and
  the PHP workers' containers down – files and worker definitions stay,
  the workers come back with PHP – and hands the project back to the dev
  server or the static document root. Over the API: `PATCH
  /api/v1/projects/{id}` with `{"php": {"version": "8.4", "config": …}}`
  adds, `{"php": {"enabled": false}}` removes.
- **Existing Node-only dev-server projects** (created before this feature
  via the unticked "Enable PHP" box or MCP `"none"`): the node and web
  containers are recreated once at the next start (new command wrapper,
  unpublished port); from then on `<project>.<base>` reaches the dev server.

A **static site** (no PHP, no Node, no Python server) is the same web
container alone: pick **Static site** in the wizard; Envoryx writes a
starter `index.html` unless you clone a repository.

### Python projects (Django, Flask, FastAPI)

Pick **Python application** on the first wizard step (or enable Python on
any project from the Runtime tab). The Python container
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
  reload – Starlette, Litestar and any ASGI app work the same way), **WSGI**
  (`gunicorn <app> --reload`) and **Other** (`python -m <module>` reading
  `HOST`/`PORT`). The application is given as `module:attribute`
  (`main:app`, `app:app`). Production mode needs gunicorn/uvicorn in the
  `.venv` (the templates install them).
- **Routing.** Without PHP the Python server is the application: the proxy
  routes `https://<project>.<base>` and every extra domain to
  `envoryx-<project>-python:<port>`, the web container's host port stays
  unpublished (the project root with `.env` and sources is never served),
  and the Domains tab's *Direct access* is the Python host port. A Node dev
  server next to Python keeps `<project>-dev.<base>` and its own host port –
  the usual Django/FastAPI backend plus Vite frontend. Next to PHP the
  Python server only has its host port; PHP stays the application.
- **Cold start.** A blank Python project has nothing to run yet: the
  container waits for the entry file (`manage.py` for Django, else
  `<module>.py` or `<module>/`, log line `envoryx: waiting for …`) instead
  of crash-looping. Pick a template, clone a repository or set the project
  up from the Python terminal; the container picks it up within seconds.
- **Templates.** *Django* (`django-admin startproject config .`, settings
  patched for the proxy – `ALLOWED_HOSTS = ["*"]`, `SECURE_PROXY_SSL_HEADER`,
  `USE_X_FORWARDED_HOST`, `CSRF_TRUSTED_ORIGINS` from the environment – and
  `dj-database-url` reading the injected `DATABASE_URL`; psycopg and
  mysqlclient installed), *Flask* (`app.py`) and *FastAPI* (`main.py`,
  docs at `/docs`). Each creates the `.venv`, installs the packages and
  pins them with `pip freeze > requirements.txt`, so *pip install* from the
  Actions tab reproduces the environment after a fresh clone.
- **Actions and workers.** Actions: `python --version`, `python -m venv
  .venv`, `pip install -r requirements.txt` (creates the venv when
  missing), `pip freeze`, `uv sync`/`uv lock` (with a `pyproject.toml`),
  Django `migrate`, `makemigrations`, `collectstatic`, `check` and `flush`
  (with a `manage.py`). Worker presets in the Python image: *Python script*
  (`python <file>`), *Python module* (`python -m <module>`), *manage.py
  command* (`rqworker`, `qcluster`, …), *Celery worker* and *Celery beat*
  (`celery -A <app> …`).
- **Debugging.** *Publish the debugpy port* (Runtime tab, server required)
  publishes port 5678 on a host port; the IDE tab shows host, port and path
  mapping plus command lines that start debugpy in front of the usual
  servers. Only the port is published – `pip install debugpy` in the
  `.venv` and start it yourself (`python -m debugpy --listen
  0.0.0.0:5678 manage.py runserver …`), then attach VS Code (`type:
  debugpy`, `request: attach`). PyCharm's *Python Debug Server* works the
  other way round (the IDE listens); use its `pydevd-pycharm` snippet with
  your workstation's address.
- **Adding or removing Python later.** The Runtime tab's Python card has
  an *Enable Python* switch; removing it takes the Python container and the
  Python workers' containers down – files, `.venv` and worker definitions
  stay. Over the API: `PATCH /api/v1/projects/{id}` with `{"python":
  {"enabled": true, "version": "3.13", "server": true, "preset": "asgi",
  "app": "main:app"}}` adds or changes, `{"python": {"enabled": false}}`
  removes.

### Bare metal

Outside Docker the proxy dials the project's published port
(`127.0.0.1:<port>`) instead of joining the project network. Binding 80/443
needs `setcap cap_net_bind_service=+ep ./envoryx` or other addresses
(`ENVORYX_PROXY_HTTP=:8080`).

## Stopping and restarting

Project operations – creating, starting, restarting, updating, deleting a
project, project backups and restores – run to completion on the server even
when the browser tab that started them is closed, the page is reloaded or the
connection drops. The UI simply shows the result on the next load.

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
Docker → Docker stop timeout*, default 10 s) – raise it and the template's
*Shutdown grace* together if you want the same behaviour.

### Orphaned resources

A container, network or volume with Envoryx labels whose project no longer
exists in the database – after restoring an older instance backup, a wiped
`/config`, or a delete that Docker only half completed – is an *orphan*. The
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
  projects keep their desired state – this is Envoryx going down, not the
  user stopping them.
- On the next start, Envoryx starts the projects that were running before –
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
previous version: pull the old image, then restore that backup from *Settings →
Instance backups*. If the old version refuses to start because the schema is
newer, its error message names the pre-migrate backup to restore by hand (see
[Instance backups](#instance-backups)).

## Keeping runtimes up to date

- **Patch releases** (e.g. 8.5.3 → 8.5.4): the `envoryx-php` images are rebuilt
  weekly from the official `php` images. A project **Restart** pulls the tag
  again and recreates the container only if the image actually changed. Until
  then the project keeps running on the previous build – nothing changes
  behind your back.
- **Rolling back**: when a restart replaced containers with a rebuilt image,
  the project's *Overview* shows *image updated <date>* next to the service
  with a **Roll back** button. It recreates the containers from the image
  they ran before (the same for MariaDB, Caddy, … – every image tag Envoryx
  pulls). The previous image is kept out of *Docker → unused images* for as
  long as a project can roll back to it. A rolled-back project stays on that
  image through further restarts (a *previous image* badge marks it) until you
  choose **Use current image**. Rolling back is a per-tag safety net, not a
  version change: for another PHP version use the Runtime tab.
- **New minor versions** (e.g. 8.6): a weekly workflow compares
  `internal/runtime/php_versions.json` with endoflife.date and Docker Hub and
  opens a pull request when a version appears, becomes stable or reaches EOL.
  Merging it builds the images and the next Envoryx image shows the version in
  the wizard. Pre-release versions are marked *preview*.

Node.js images (`envoryx-node:*`) follow the same scheme with `node_versions.json`,
Python images (`envoryx-python:*`, official `python:<v>-slim-bookworm` plus uv,
git and build dependencies) with `python_versions.json`; a project's Node or
Python version is changed on the Runtime tab like the PHP version.

## Git deploy key

For SSH repositories Envoryx generates an Ed25519 key pair on first use under
`/config/ssh/`. Copy the public key from **Settings → Git deploy key** (or the
project's Git tab) into your repository as a read-only deploy key. Private
HTTPS repositories use an access token per project instead (GitHub:
fine-grained PAT with *Contents: read*; GitLab: username `oauth2` + token).

## Backups

Project backups (database dump, files, configuration) are created from the
project's **Backups** tab and stored as plain directories, one per backup,
under `<backups dir>/<project>/`. Changing a database's version takes a
database backup automatically first (source *upgrade*); if no dump can be
taken – the project is stopped – the upgrade is refused, because the server
rewrites its data directory on the first start and cannot go back.

By default the backups directory is `/config/backups`, i.e. on the appdata
share. Backups are large and rarely read, so you may want them on the array
instead of the cache SSD: mount a host directory at `/backups` (Unraid: add
the optional *Backups* path in the template, e.g. `/mnt/user/backups/envoryx`;
Compose: uncomment the `/backups` volume) and Envoryx uses it automatically.
`ENVORYX_BACKUPS_DIR` overrides the location explicitly. To move existing
backups, stop Envoryx, move the contents of `/config/backups/` into the new
directory and start again – a warning is logged at startup while backups are
left behind in the old location.

Include the backups directory in your regular off-machine backup (e.g. the
Unraid Appdata Backup plugin or an rsync job), use the per-backup download, or
let Envoryx copy the backups offsite itself (see *Offsite backups* below).

**Scheduled backups**: Backups tab → *Scheduled backups*: daily or weekly at
a given hour (server local time – set `TZ` on the container for your zone),
keep the last N scheduled backups (manual ones are never deleted), optionally
including `vendor/`, `node_modules/` and the framework build caches
(`.next/`, `.nuxt/`, `.output/`), which are skipped by default because they
are large and reproducible. Failures raise a notification.

### Instance backups

*Settings → Instance backups* snapshots Envoryx itself: the SQLite database
(accounts, sessions, API tokens, projects and their service settings, domains,
workers, settings), the local CA, the SSH host key and deploy keys,
notification settings and the generated per-project configuration. Project
files and Docker volumes are **not** included – that is what project backups
are for. A backup is a single `envoryx-<id>.tar.gz` under
`<backups dir>/_instance/` (`instance.json` with version/schema, `envoryx.db`
as a consistent `VACUUM INTO` copy, `config/…`).

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
  backup, the `pre-restore` safety copy and who requested it – the audit rows
  written after the backup was taken are gone with the old database.
- A backup from a **newer** Envoryx (higher schema version) is refused; an
  older one is migrated forward on start (with its own `pre-migrate` backup).

Manual restore without the UI (e.g. Envoryx does not start): stop the
container, unpack the archive – `envoryx.db` to `/config/envoryx.db` (delete
`envoryx.db-wal`/`-shm` if present), `config/*` over `/config/` – and start
again.

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
Credentials live in `/config/offsite.json` (mode 0600) and are never sent back
to the browser.

**What goes up.** Per target:

- *Scheduled project backups* – every scheduled backup of every project is
  copied once it is taken. *Keep per project* rotates the scheduled copies on
  the target (0 = keep all); copies made by hand are never rotated away.
- *Daily instance backup* – at the chosen hour Envoryx takes an instance
  backup (kind `scheduled`, the last 5 stay locally) and copies it. This is
  what a fresh Envoryx needs after the host is gone.
- Everything else on request: *Copy offsite* next to a backup (project or
  instance), *Also copy offsite* when creating one, `envoryx backup create
  --offsite` or `envoryx backup offsite <project> <backup>`.

Uploads run in the background. A failed one is retried after 5, 15 and 45
minutes and 2 hours, then stays failed (the button tries again); every
failure raises the *Backup failed* notification. The Backups tab shows each
copy's state next to the backup.

**Encryption** is a per-target switch and on by default. Archives are
encrypted with [age](https://age-encryption.org) and a scrypt passphrase
before they leave the host, so the provider only sees `.age` files – backups
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
tab lists what the target holds for the project – also backups that were
deleted here – and *Fetch* puts one back into the local list, from where it is
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

## Database browser (Adminer)

*Settings → Database browser* switches on an in-browser database tool for
projects with MariaDB, MySQL or PostgreSQL (MongoDB is not supported by the
Adminer image; use the published port with Compass). Nothing runs until the
first click on **Open database** in a project's Database tab: Envoryx then
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

Projects can get S3-compatible object storage (the **Services** tab, or
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
| `S3_PUBLIC_ENDPOINT` | –                          | `https://<project>-s3.<base>`           |

A Laravel `s3` disk works without further configuration
(`FILESYSTEM_DISK=s3`); code written for any S3-compatible provider runs
unchanged once it reads endpoint, keys and bucket from these variables and
uses path-style addressing.

**Reaching the storage from the browser.** The embedded proxy serves the S3
API as `<project>-s3.<base domain>` (covered by the same wildcard DNS and
certificate as the project), so `Storage::url()` links, public assets and
direct uploads work in the browser. Presigned URLs meant for a browser must
be signed against that public endpoint (`S3_PUBLIC_ENDPOINT`) – the signature
covers the host name, a URL signed for `s3:9000` is useless outside the
project network. The S3 API and the web console are also published on host
ports (shown in the Services tab) for local tools such as `aws s3 --endpoint-url`.

**Public read.** Real providers honour `public-read` ACLs; RustFS accepts but
ignores them. Envoryx therefore puts a bucket policy on the bucket that lets
anyone read every object, switched on by default so `Storage::url()` behaves
as it would in production. Turn *Anyone may read objects* off in the Services
tab to test that nothing relies on it; then only presigned URLs and
authenticated requests work.

The console (RustFS's own UI) opens from the Services tab; sign in with the
project's access keys. Removing the object storage deletes the bucket volume
and needs the bucket name as confirmation.

**Backups.** Project backups can include the bucket (on by default for manual
and scheduled backups when the project has object storage): every object is
stored as a plain file in `storage.tar.gz`, named by its key, with the content
type kept as an `user.mime_type` extended attribute – readable with any tar,
independent of the server's on-disk format. Restoring uploads the objects
again, optionally emptying the bucket first; the storage container must be
running for both.

## Logs

The Logs tab has two views per container. *Live* follows the output as it
comes. *History* searches the past output: a time range (a preset or
from/to), a text search and a level filter, a chart of lines, warnings and
errors over time – a click on a bar zooms into that slot – and the most
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
it is written, and the History view reads from there – across restarts and
recreated containers. The footer says which source a result came from.

- Output written while Envoryx was stopped is picked up when it comes back,
  as long as Docker still has it; nothing is stored twice.
- Days are UTC. Finished days are compressed (gzip), days older than the
  retention (default 7) are deleted, and when the history grows beyond its
  limit (default 1 GB for all projects together) the oldest days go first.
  Both are set in *Settings → General → Log history*, which also shows the
  space in use.
- Deleting a project deletes its history. *Delete stored logs* empties the
  whole history; lines Docker still holds are not collected again.
- The history is not part of instance backups.
- Switched off, nothing new is stored and the Logs tab reads the containers
  again, as before.

## Workers (queues, schedulers)

Workers tab: add long-running processes from a preset list – Laravel
`schedule:work`, `queue:work`/`queue:listen` (queue names), Horizon,
Reverb, Symfony `messenger:consume` (transports) and Scheduler, a PHP script
or a composer script (PHP image); npm scripts and Node scripts (Node
image); Python scripts and modules, Django management commands, Celery
worker and beat (Python image). Every worker is its own container
(`envoryx-<project>-worker-<name>`) from the image of the runtime its
preset names, runs as `PUID:PGID` with the project's environment (and
php.ini for PHP, the venv `PATH` for Python), restarts automatically
(Docker `unless-stopped`) and follows start/stop/restart of the project.
The Workers tab offers only the presets whose runtime the project has.
`queue:work` stops after an hour (`--max-time`) so code changes
are picked up on the automatic restart; use `queue:listen` for instant
reloads. Logs are in the Logs tab; up to 10 workers per project.

## IDE integration (PhpStorm, WebStorm, VS Code)

Every project has an **IDE** tab with all values ready to copy.

**Remote interpreter over SSH.** Envoryx runs an SSH server on port 2222
(publish it, or use the container's own IP on `br0`). User name = project
slug (`shop`): it lands in the project's application container – PHP when
the project has PHP, else Python, else Node. Projects with several runtimes
also accept `shop.php`, `shop.python` and `shop.node` to pick one
explicitly (the IDE tab lists these rows only then). Password = an API token from Settings → API tokens, or a
public key stored under Settings → SSH access. Each session is a
`docker exec` into that container as the project owner – there is no shell
on the host. SFTP exposes `/var/www/html` (the project) and `/home/envoryx`
(a persistent home for tool caches and IDE helpers).

- PhpStorm: *Settings → PHP → CLI Interpreter → … → From Docker, Vagrant,
  VM, WSL, Remote… → SSH*; PHP path `/usr/local/bin/php`, helpers path
  `/home/envoryx/.phpstorm_helpers`, path mapping *project folder* →
  `/var/www/html`. Afterwards PHPUnit/Pest, Composer and Artisan run inside
  the container from the IDE.
- WebStorm (Node-only project, or `shop.node` next to PHP): *Settings →
  Languages & Frameworks → Node.js → Node interpreter → Add… → SSH*, host
  and port from the IDE tab, user `shop`, Node path `/usr/local/bin/node`,
  project path `/var/www/html`. npm scripts, the test runner and the
  debugger then run in the container.
- PyCharm (Python project, or `shop.python` next to PHP): *Settings →
  Project → Python Interpreter → Add Interpreter → On SSH…*, host and port
  from the IDE tab, user `shop`, interpreter `/var/www/html/.venv/bin/python`,
  project path `/var/www/html`. pytest, manage.py and pip then run in the
  container.
- VS Code: Remote-SSH works the same way (`ssh -p 2222 shop@<host>`); open
  `/var/www/html` as the remote folder.

A static project (no PHP, Python or Node) has no application container, so
SSH sessions are refused for it.

The project must be running for sessions to open. Commands are logged to
the audit log (`ssh.exec`), failed logins are rate limited per IP.

### JetBrains Gateway (optional)

Gateway runs the complete IDE backend on the server and connects a thin
client. In Envoryx this is opt-in per project (IDE tab → *Allow JetBrains
Gateway*): it enables SSH port forwarding into the container and mounts a
shared backend cache (`/config/jetbrains`, ~1.5 GB per IDE version,
downloaded once). The backend runs as the project owner inside the
application container – PHP, else Python, else Node (user `<slug>`;
`<slug>.python` / `<slug>.node` pick one next to PHP) – and needs 2–4 GB RAM plus CPU while
indexing – nothing runs until you connect. Envoryx ships no JetBrains
software: Gateway itself is free, the IDE backend is uploaded by your
Gateway client and licensed through it – whoever connects needs a valid
subscription for that IDE (PhpStorm, WebStorm or All Products Pack), the
server needs nothing. Gateway → *SSH → New connection*
with the values from the IDE tab, choose PhpStorm/WebStorm, project
directory `/var/www/html`. Close the project in Gateway or use *Stop IDE
backend* to free the memory. Small NAS boxes: leave it off.

The tunnel to the backend needs `socat` in the runtime image (PHP and Node
images since September 2026, Python images from the start). Troubleshooting:

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

Runtime tab → PHP → **Xdebug**: enables step debugging for that project
(port 9003, mode `debug,develop`, `start_with_request=yes`). Xdebug connects
back to the machine that made the request – behind Envoryx's proxy that
address comes from `X-Forwarded-For` – and falls back to the *developer
machine* set in Settings → Project links (or a per-project override). Map
`/var/www/html` to your project folder in the IDE; the tab shows the exact
PhpStorm/VS Code settings. Turn it off when you are done: it slows PHP down.

## Notifications

Settings → **Notifications**: pick a channel (ntfy, Discord or Slack
webhook, Telegram bot, e-mail via SMTP, or a generic JSON webhook), choose
the events and send a test. Events: a project that should be running is
stopped/broken (and when it recovers), project creation failed, backup
failed, Let's Encrypt renewal failed/succeeded, Envoryx started, Envoryx
failed (refused to start – corrupt database, network filesystem, failed
migration – or a background task crashed and was restarted). Repeats are
throttled (unhealthy project once per 6 h, failed renewal once per day). The
failed-start notification is sent before the process exits and needs no
database, only the channel configured in `/config/notify.json`.
Secrets live in `/config/notify.json` (0600). SMTP authentication requires
STARTTLS or TLS.

## AI assistants (MCP)

Envoryx ships an MCP server at `/mcp` (streamable HTTP). Create a token under
**Settings → API tokens & MCP**; the page shows a ready-to-paste client
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
version + extensions, database, Redis, Memcached, Mailpit, RabbitMQ, Meilisearch, Typesense, OpenSearch, object storage, Node, Python, git clone, env),
start/stop/restart, get logs, list/run actions (composer, artisan, npm …),
list/create databases, list/create backups, add domain. Deleting projects,
dropping databases and restoring backups are intentionally not exposed –
do those in the UI. Example prompt: *"Create a Laravel project called
test-api with PHP 8.4, MariaDB and Redis, then run composer install."*

Projects without PHP: pass `phpVersion: "none"` plus `nodeVersion`,
`nodeDevServer: true` and `nodePreset` (`vite`, `next`, `nuxt`, `generic`;
optional `nodeScript`, `nodePort`, `nodePackageManager`), or a Node template
(`vite`, `next`, `nuxt`) which fills the dev-server defaults. The result
carries `serves` (`php`/`node`/`static`), `devUrl` and a `directUrl` that
points at the node host port while the dev server serves the project.
Example prompt: *"Create a Node.js project called dashboard from the Nuxt
template, no PHP, then show me its logs."*

Python projects: pass `phpVersion: "none"` plus `pythonVersion`,
`pythonServer: true` and `pythonPreset` (`django`, `flask`, `asgi`, `wsgi`,
`module`; optional `pythonApp`, `pythonPort`, `pythonMode`), or a Python
template (`django`, `flask`, `fastapi`) which fills the server defaults.
`serves` is `python` then and `directUrl` points at the Python host port.
Example prompt: *"Create a FastAPI project called inventory-api with
PostgreSQL, no PHP, then run pip install."*

### Scripting the REST API

The same tokens authenticate the REST API (`/api/v1/...`) for scripts, CI
jobs and the [command line](#command-line) – send them as
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
| `operate` | Working with existing projects: start/stop/restart, image rollback, actions (composer, artisan …), creating backups and databases, git, domains, SSH/SFTP, terminal, database browser. Default for new tokens. |
| `admin`   | Everything a browser session may do: creating and deleting projects, settings, TLS, notifications, instance backups, image clean-up, restores, dropping databases.  |

A token can additionally be **limited to particular projects**. It then sees
only those in listings, every other project answers `403` (REST) or "no project
matches" (MCP), instance-wide endpoints (dashboard, settings, Docker overview)
are closed, and it cannot create projects – regardless of its scope. Use this
for an assistant that should work on one project only.

Refusals carry the reason (`this token has read scope, the operation needs
operate`) so scripts and assistants can tell what kind of token they need.
`GET /api/v1/auth/me` shows the calling token's name, scope and projects.

No token can change the password or create/revoke tokens – those need a
browser session. Audit entries record `user (token: name)`. A request that
presents an invalid or revoked token is rejected even if a valid session
cookie is also sent. Tokens created before scopes existed keep full access
(`admin`, all projects).

## Command line

The Envoryx binary is also its own client. `envoryx project …`, `envoryx
backup …`, `envoryx db …` and `envoryx git …` talk to a running server over the
REST API with an API token, which makes them equally at home in an SSH session,
a cron job or a CI pipeline. On the host the container's own binary does the job:

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
wants. `envoryx whoami` shows whose token it is and what it may do, `envoryx
logout` forgets it again (the token itself is revoked under *Settings → API
tokens*). Without a stored configuration, `ENVORYX_URL` and `ENVORYX_TOKEN`
work just as well – handy in CI, where nothing should be written to disk.

### What it can do

```sh
envoryx project list                                  # name, slug, state, URL
envoryx project show shop                             # services, versions, ports, git
envoryx project create "Shop" --php 8.4 --database mariadb --template laravel --start
envoryx project duplicate shop "Shop Test"            # config, files and database
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
envoryx git status|pull shop                          # and: git checkout shop main
```

A project is named by its name, its slug or its id. `--json` hands the API's
own answer to `jq` instead of a table; `--service` picks a container other
than the project's application container (`php`, `python`, `node`, `web`,
`database`, `redis`, `memcached`, `mailpit`, `rabbitmq`, `meilisearch`, `typesense`,
`opensearch`, `opensearch-dashboards`, `storage`, `worker:<id>`). `envoryx project
create --from-json file.json` sends a create request the flags do not cover
(everything the wizard offers), and flags given alongside it win.

Every command exits `0` on success, `1` on failure and `2` on a usage error.
`envoryx project exec` passes the command's own exit code on, so
`envoryx project exec shop -- php artisan migrate --force || rollback` does
what it looks like. Its stdout and stderr stay apart, input is piped in
(`envoryx project exec shop -- sh -c "cat > /tmp/x" < file`, up to 512 KiB),
and the command runs as the project owner in the project directory – the same
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
clones the repository itself – the `origin` remote and the checked-out branch,
over the same credentials as the wizard (deploy key for SSH URLs,
`--git-token` for private HTTPS) – creates the project exactly as described and
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
redis: true                      # or {version: "8", exposePort: true}
mailpit: true                    # also memcached, rabbitmq, meilisearch, typesense,
opensearch: {dashboards: true}   # opensearch, storage: {publicRead: false}
domains: [api.shop.example.com]
env:
  APP_ENV: local
secrets: [STRIPE_SECRET]         # names only – values never go into the repository
workers:
  - {name: queue, preset: "laravel:queue", arg: default}
cron:
  - {name: prune, schedule: "0 3 * * *", command: php artisan model:prune, timeout: 5m}
```

`node:` and `python:` take the fields of the wizard (`devServer`, `preset`,
`port`, `script` …; `server`, `preset`, `app`, `debug` …). A setting left out
means Envoryx's default, so the file is the whole desired state: `web:` missing
means Caddy, `extensions:` missing the default set. Unknown keys are errors –
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
cron job the file no longer has is listed as *kept*. With `--prune` it goes –
for a database or a service with a volume (Redis, RabbitMQ, the search engines,
storage) together with its data. Another database `type` counts as such a
removal. A database version lower than the project's is never applied, as the
data format does not go back.

In the web interface the *Git* tab shows the project as `envoryx.yml` (copy,
download, save into the project directory) and compares the file in the project
directory with the project – after a `git pull` that brought a changed
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

The file is the one from *Settings → TLS → Download CA certificate*
(`ENVORYX_CA_CERT` does the same). `--insecure` skips verification for a quick
look on a trusted network; with a Let's Encrypt certificate neither is needed.

## Lost access

The credentials for the web interface can be reset from a shell inside the
running container; nothing else is touched and no restart is needed:

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
projects, settings and backups alone – open the web interface afterwards and
create the administrator account again. Every command is written to the audit
log with the user `cli`. On Unraid the container's console (*Docker →
Envoryx → Console*) is the same shell.

Note that failed sign-in attempts are rate-limited per address for up to
15 minutes; if you tried a few wrong passwords just before the reset, wait a
moment.

## Health check

`GET /api/v1/health` returns `{"status":"ok","docker":true,"database":true,…}`
without authentication. The image ships a `HEALTHCHECK` that calls
`envoryx healthcheck`.

The check is a *liveness* check: it fails (503, `"status":"unavailable"`)
only when the database is unusable. An unreachable Docker engine is reported
as `"docker":false` with `"status":"degraded"` but still answers 200 –
Envoryx keeps running and retries the engine on demand, and restarting the
Envoryx container would not fix a Docker problem. Tools that restart
unhealthy containers (autoheal) therefore do not loop on it.
