import { Bug, Database, FolderSync, KeyRound, Mail, MonitorSmartphone, Rabbit, TerminalSquare } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { Link } from "react-router-dom";
import { useMutation } from "@tanstack/react-query";
import { api } from "@/api/client";
import { useDatabaseInfo, useExtraServices, useSettings, useUpdateProject } from "@/api/hooks";
import { OperationHint } from "@/components/OperationsTray";
import { appKindOf, type NodeConfig, type Operation, type PHPConfig, type Project, type PythonConfig } from "@/api/types";
import { Alert, Button, Card, CardHeader, Checkbox, Code } from "@/components/ui";
import { CopyButton, CopyRow } from "./DatabaseTab";
import { proxyUrl } from "./DomainsTab";
import { errorText } from "@/lib/errors";

/** Everything an IDE needs, ready to copy: SFTP deployment, SSH interpreter, Xdebug server, database, mail. */
export function IdeTab({ project: p }: { project: Project }) {
  const { t } = useTranslation();
  const settings = useSettings();
  const hasDb = p.services.some((s) => s.kind === "database" && s.enabled);
  const db = useDatabaseInfo(p.id, hasDb);
  const extras = useExtraServices(p.id);
  const s = settings.data;
  const host = s?.publicHost || window.location.hostname;
  const projectsHost = s?.hostPath ? (s.hostPath.overrides[s.projectsDir] ?? s.hostPath.detected[s.projectsDir]) : undefined;
  const hostDir = projectsHost ? `${projectsHost}/${p.path}` : "<projects share>/" + p.path;
  const php = p.services.find((x) => x.kind === "php" && x.enabled);
  const hasPhp = !!php;
  const hasNode = p.services.some((x) => x.kind === "node" && x.enabled);
  const hasPython = p.services.some((x) => x.kind === "python" && x.enabled);
  // The bare SSH user lands in the application container: PHP when present, else Python, else Node.
  const app = p.appService ?? appKindOf(p);
  const runtimeCount = [hasPhp, hasPython, hasNode].filter(Boolean).length;
  const phpCfg = (php?.config ?? {}) as unknown as Partial<PHPConfig>;
  const nodeCfg = (p.services.find((x) => x.kind === "node" && x.enabled)?.config ?? {}) as unknown as Partial<NodeConfig>;
  const pyCfg = (p.services.find((x) => x.kind === "python" && x.enabled)?.config ?? {}) as unknown as Partial<PythonConfig>;
  const hostname = p.hostnames[0] ?? `${p.slug}.test`;
  const ssh = s?.ssh;
  const sshHost = s?.proxy?.address || host;
  const mailpit = extras.data?.find((e) => e.kind === "mailpit");
  const rabbitmq = extras.data?.find((e) => e.kind === "rabbitmq");
  const update = useUpdateProject(p.id);
  const [gwMsg, setGwMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const stopBackend = useMutation({
    mutationFn: () => api.projects.stopIDEBackend(p.id),
    onSuccess: (r) => setGwMsg({ tone: "green", text: r.stopped > 0 ? t("IDE backend stopped.") : t("No IDE backend was running.") }),
    onError: (err) => setGwMsg({ tone: "red", text: errorText(err, t, t("Request failed")) }),
  });

  const phpXml = `<?xml version="1.0" encoding="UTF-8"?>
<project version="4">
  <component name="PhpProjectServersManager">
    <servers>
      <server host="${hostname}" id="envoryx-${p.slug}" name="${hostname}" use_path_mappings="true">
        <path_mappings>
          <mapping local-root="$PROJECT_DIR$" remote-root="/var/www/html" />
        </path_mappings>
      </server>
    </servers>
  </component>
</project>`;

  const jdbc = db.data
    ? db.data.type === "postgresql"
      ? `jdbc:postgresql://${host}:${db.data.hostPort || 5432}/${db.data.database}`
      : db.data.type === "mongodb"
        ? `mongodb://${db.data.username}@${host}:${db.data.hostPort || 27017}/${db.data.database}?authSource=admin`
        : `jdbc:${db.data.type === "mysql" ? "mysql" : "mariadb"}://${host}:${db.data.hostPort || 3306}/${db.data.database}`
    : "";

  return (
    <div className="space-y-6">
      {ssh?.enabled && ssh.port > 0 && app && (
        <Card>
          <CardHeader
            title={
              <span className="flex items-center gap-2">
                <FolderSync className="size-4 text-accent-500" aria-hidden /> {t("Project files (SFTP)")}
              </span>
            }
            description={t("Open the project in PhpStorm, WebStorm & co. without a network share: the SSH server also speaks SFTP, with the container's paths. The IDE keeps a local copy and uploads every change on save – this works while the project is stopped, too.")}
          />
          <div className="p-5">
            <dl>
              <CopyRow label={t("Type")} value="SFTP" mono={false} />
              <CopyRow label={t("Host")} value={sshHost} />
              <CopyRow label={t("Port")} value={String(ssh.port)} />
              <CopyRow label={t("User")} value={p.slug} />
              <CopyRow label={t("Password")} value={t("<API token from Settings → API tokens>")} mono={false} />
              <CopyRow label={t("Root path")} value="/var/www/html" />
              {s?.proxy?.enabled && proxyUrl(hostname, s.proxy) && <CopyRow label={t("Web server URL")} value={proxyUrl(hostname, s.proxy)} />}
            </dl>
            <ol className="mt-3 list-decimal space-y-1 pl-5 text-sm text-muted">
              <li>{t("Welcome screen: New Project from Existing Files… (with a project open: File → New Project from Existing Files…) → “Web server is on remote host, files are accessible via FTP/SFTP/FTPS/WebDAV”.")}</li>
              <li>{t("Project name and an empty local folder for the copy. Deployment options “Custom”, then on the next page set “Upload changed files automatically to the default server” to “Always”.")}</li>
              <li>{t("Add Remote Server: type SFTP. “…” next to SSH configuration, then “+”: host, port and username from above, authentication type “Password” (the API token) or “Key pair”. Root path and web server URL as above.")}</li>
              <li>{t("Choose Remote Path: select the server entry and click “Project Root”, leave the web path empty, Create. The IDE downloads the files.")}</li>
              <li>{t("Files changed inside the container (composer install, npm install, generated code) come back with a right click on the project folder → Deployment → Download from … – download vendor once for code completion.")}</li>
            </ol>
            <p className="mt-3 text-xs text-subtle">
              {t("Code already on your machine (e.g. a Git clone)? Settings → Build, Execution, Deployment → Deployment → + → SFTP with the same values, “Use as Default”, then Tools → Deployment → Automatic Upload.")}
            </p>
            <p className="mt-2 text-xs text-subtle">
              {t("On the first connection the IDE shows the host key as MD5 – compare it with “Host key (MD5)” under Remote interpreter. No local copy wanted? Open the network share directly or use JetBrains Gateway.")}
            </p>
          </div>
        </Card>
      )}

      <Card>
        <CardHeader
          title={
            <span className="flex items-center gap-2">
              <TerminalSquare className="size-4 text-accent-500" aria-hidden /> {t("Remote interpreter (SSH)")}
            </span>
          }
          description={
            hasPhp
              ? t("Run PHP, Composer, PHPUnit and Artisan inside the project container from your IDE. PhpStorm: Settings → PHP → CLI Interpreter → “…” → “+” → From Docker, Vagrant, VM, WSL, Remote… → SSH, then Path mappings: the project folder → /var/www/html. VS Code: Remote-SSH. Plain terminal: ssh.")
              : app === "python"
                ? t("Run python, pip and pytest inside the project container from your IDE. PyCharm Pro: Settings → Python → Interpreter → Add Interpreter → On SSH…; in the last step “Select existing” with the interpreter path from below, and under Target-Specific Properties Sync folders: the project folder → /var/www/html. VS Code: Remote-SSH. Plain terminal: ssh.")
                : t("Run node, npm and your test runner inside the project container from your IDE. WebStorm: Settings → Languages & Frameworks → JavaScript Runtime → Node runtime “…” → “+” → Add Remote… → SSH, then in the run configuration Path mappings: the project folder → /var/www/html. VS Code: Remote-SSH. Plain terminal: ssh.")
          }
        />
        <div className="p-5">
          {!ssh?.enabled ? (
            <Alert tone="amber">{t("The SSH server is disabled (ENVORYX_SSH is empty).")}</Alert>
          ) : ssh.port === 0 ? (
            <Alert tone="amber">{t("The SSH port 2222 is not published on the host – add a port mapping 2222:2222 to the Envoryx container.")}</Alert>
          ) : !app ? (
            <Alert tone="gray">{t("This project has no application container – SSH sessions need PHP, Python or Node.js.")}</Alert>
          ) : (
            <dl>
              <CopyRow label={t("Host")} value={sshHost} />
              <CopyRow label={t("Port")} value={String(ssh.port)} />
              <CopyRow label={t("User")} value={p.slug} />
              {runtimeCount > 1 && hasPhp && <CopyRow label={t("User (PHP)")} value={`${p.slug}.php`} />}
              {runtimeCount > 1 && hasPython && <CopyRow label={t("User (Python)")} value={`${p.slug}.python`} />}
              {runtimeCount > 1 && hasNode && <CopyRow label={t("User (Node)")} value={`${p.slug}.node`} />}
              <CopyRow label={t("Password")} value={t("<API token from Settings → API tokens>")} mono={false} />
              {hasPhp && <CopyRow label={t("PHP path")} value="/usr/local/bin/php" />}
              {hasPython && <CopyRow label={t("Python path")} value="/var/www/html/.venv/bin/python" />}
              {hasPython && <CopyRow label={t("Python path (without .venv)")} value="/usr/local/bin/python" />}
              {hasNode && <CopyRow label={t("Node path")} value="/usr/local/bin/node" />}
              <CopyRow label={t("Project path")} value="/var/www/html" />
              {hasPhp && <CopyRow label={t("Helpers path")} value="/home/envoryx/.phpstorm_helpers" />}
              <CopyRow label={t("Host key")} value={ssh.fingerprint} />
              {ssh.fingerprintMd5 && <CopyRow label={t("Host key (MD5)")} value={ssh.fingerprintMd5} />}
              <CopyRow label="ssh" value={`ssh -p ${ssh.port} ${p.slug}@${sshHost}`} />
            </dl>
          )}
          {app && (
            <p className="mt-3 text-xs text-subtle">
              {t("Authentication: an")} <Link to="/settings?tab=access" className="underline">{t("API token")}</Link> {t("as password, or your public key under Settings → SSH access. Sessions run as the project owner inside")} <Code>envoryx-{p.slug}-{app}</Code>; {t("the project must be running.")}
            </p>
          )}
        </div>
      </Card>

      <Card>
        <CardHeader
          title={
            <span className="flex items-center gap-2">
              <MonitorSmartphone className="size-4 text-accent-500" aria-hidden /> {t("JetBrains Gateway (optional)")}
            </span>
          }
          description={t("Run the full PhpStorm/WebStorm backend inside the project container and work with the thin client. Needs a capable server: 2–4 GB RAM and CPU per open project. Nothing runs until you connect.")}
        />
        <div className="space-y-3 p-5">
          {gwMsg && <Alert tone={gwMsg.tone}>{gwMsg.text}</Alert>}
          <Checkbox
            label={t("Allow JetBrains Gateway for this project")}
            description={t("Enables SSH port forwarding into the container and mounts a shared IDE backend cache (/config/jetbrains, downloaded once for all projects). Recreates the application containers.")}
            checked={!!p.ideGateway}
            disabled={update.isPending}
            onChange={(e) => {
              setGwMsg(null);
              const enable = e.target.checked;
              update.mutate(
                { ideGateway: enable },
                {
                  onSuccess: () => setGwMsg({ tone: "green", text: enable ? t("JetBrains Gateway enabled – the container was recreated.") : t("JetBrains Gateway disabled – the container was recreated.") }),
                  onError: (err) => setGwMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
                },
              );
            }}
          />
          {update.isPending && <OperationHint op={p.status.operation ?? pendingUpdate(p)} />}
          {p.ideGateway && ssh?.enabled && ssh.port > 0 && (
            <>
              <ol className="list-decimal space-y-1 pl-5 text-sm text-muted">
                <li>Gateway → <span className="text-fg">SSH → New connection</span>: {t("host")} <Code>{sshHost}</Code>, {t("port")} <Code>{ssh.port}</Code>, {t("user")} <Code>{p.slug}</Code>, {t("password = API token (or key).")}</li>
                <li>
                  {hasPhp ? (
                    <>
                      {t("IDE: PhpStorm (or WebStorm with user")} <Code>{p.slug}.node</Code>);
                    </>
                  ) : (
                    <>
                      {t("IDE: WebStorm with user")} <Code>{p.slug}</Code>;
                    </>
                  )}{" "}
                  {t("project directory")} <Code>/var/www/html</Code>.
                </li>
                <li>{t("Gateway installs the backend into")} <Code>/home/envoryx/.cache/JetBrains</Code> {t("(shared cache) and opens the thin client.")}</li>
              </ol>
              <p className="text-xs text-subtle">{t("Close the project in Gateway when you are done, or stop the backend here to free memory on the server.")}</p>
              <Button size="sm" onClick={() => { setGwMsg(null); stopBackend.mutate(); }} loading={stopBackend.isPending}>
                {t("Stop IDE backend")}
              </Button>
            </>
          )}
        </div>
      </Card>

      {hasPhp && (
        <Card>
          <CardHeader
            title={
              <span className="flex items-center gap-2">
                <Bug className="size-4 text-accent-500" aria-hidden /> Xdebug
              </span>
            }
            description={phpCfg.xdebug ? t("Enabled ({{mode}}). PhpStorm: Settings → PHP → Servers.", { mode: phpCfg.xdebugMode ?? "always" }) : t("Not enabled – switch it on in the Runtime tab. Values below apply once enabled.")}
          />
          <div className="p-5">
            <dl>
              <CopyRow label={t("Server name")} value={hostname} />
              <CopyRow label={t("Server host")} value={hostname} />
              <CopyRow label={t("Debug port")} value="9003" />
              <CopyRow label={t("IDE key")} value={phpCfg.xdebugIdeKey || "PHPSTORM"} />
              <CopyRow label={t("Path mapping")} value={`${hostDir} → /var/www/html`} />
            </dl>
            <div className="mt-3">
              <div className="flex items-center justify-between">
                <p className="text-xs text-muted"><Code>.idea/php.xml</Code> {t("(server + mapping; adjust $PROJECT_DIR$ if the IDE project is not the project root):")}</p>
                <CopyButton value={phpXml} label="php.xml" />
              </div>
              <pre className="mt-1 overflow-x-auto rounded-md bg-muted p-2 font-mono text-[11px]">{phpXml}</pre>
            </div>
          </div>
        </Card>
      )}

      {hasNode && (
        <Card>
          <CardHeader
            title={
              <span className="flex items-center gap-2">
                <Bug className="size-4 text-accent-500" aria-hidden /> {t("Node.js debugging")}
              </span>
            }
            description={
              nodeCfg.inspect && nodeCfg.inspectHostPort
                ? t("The inspector port is published. Start the inspector in your script and attach from the IDE with the values below.")
                : t("Not enabled – switch on “Publish the Node.js inspector port” in the Runtime tab (dev server required). Values below apply once enabled.")
            }
          />
          <div className="p-5">
            <dl>
              <CopyRow label={t("Attach to host")} value={host} />
              <CopyRow label={t("Attach to port")} value={String(nodeCfg.inspectHostPort ?? "")} />
              <CopyRow label={t("Inspector inside the container")} value={`0.0.0.0:${nodeCfg.inspectPort ?? 9229}`} />
              <CopyRow label={t("Path mapping")} value={`${hostDir} → /var/www/html`} />
            </dl>
            <p className="mt-3 text-xs text-muted">
              {t("Only the port is published – the inspector has to be started by your script, otherwise NODE_OPTIONS would attach the debugger to npm instead of your app. Examples for package.json:")}
            </p>
            <pre className="mt-1 overflow-x-auto rounded-md bg-muted p-2 font-mono text-[11px]">{nodeDebugExamples(nodeCfg.inspectPort ?? 9229)}</pre>
            <p className="mt-2 text-xs text-subtle">
              {t("WebStorm: Run → Edit Configurations → Attach to Node.js/Chrome with host and port from above. VS Code: a launch.json entry of type node with request attach, address and port from above, localRoot/remoteRoot as the path mapping. Next.js opens the inspector of its server process one port higher (9230): publish that port instead when debugging server code.")}
            </p>
          </div>
        </Card>
      )}

      {hasPython && (
        <Card>
          <CardHeader
            title={
              <span className="flex items-center gap-2">
                <Bug className="size-4 text-accent-500" aria-hidden /> {t("Python debugging (debugpy)")}
              </span>
            }
            description={
              pyCfg.debug && pyCfg.debugHostPort
                ? t("The debugpy port is published. Start debugpy in your application and attach from the IDE with the values below.")
                : t("Not enabled – switch on “Publish the debugpy port” in the Runtime tab. Values below apply once enabled.")
            }
          />
          <div className="p-5">
            <dl>
              <CopyRow label={t("Attach to host")} value={host} />
              <CopyRow label={t("Attach to port")} value={String(pyCfg.debugHostPort ?? "")} />
              <CopyRow label={t("debugpy inside the container")} value={`0.0.0.0:${pyCfg.debugPort ?? 5678}`} />
              <CopyRow label={t("Path mapping")} value={`${hostDir} → /var/www/html`} />
            </dl>
            <p className="mt-3 text-xs text-muted">
              {t("Only the port is published – debugpy has to be started by your application (pip install debugpy in the .venv). Examples:")}
            </p>
            <pre className="mt-1 overflow-x-auto rounded-md bg-muted p-2 font-mono text-[11px]">{pythonDebugExamples(pyCfg.debugPort ?? 5678)}</pre>
            <p className="mt-2 text-xs text-subtle">
              {t("VS Code: a launch.json entry of type debugpy with request attach, connect host/port from above and pathMappings localRoot/remoteRoot. PyCharm: Run → Edit Configurations → Python Debug Server listens on your machine instead – use its pydevd-pycharm snippet with your workstation's address as the host.")}
            </p>
          </div>
        </Card>
      )}

      {hasDb && (
        <Card>
          <CardHeader
            title={
              <span className="flex items-center gap-2">
                <Database className="size-4 text-accent-500" aria-hidden /> {t("Database tool / DataGrip")}
              </span>
            }
            description={db.data?.hostPort ? t("Connect from your machine through the published port.") : t("Publish the database port in the Database tab to connect from your machine.")}
          />
          <div className="p-5">
            {db.data && (
              <dl>
                <CopyRow label={t("Type")} value={db.data.type} mono={false} />
                <CopyRow label={t("Host")} value={host} />
                <CopyRow label={t("Port")} value={db.data.hostPort ? String(db.data.hostPort) : t("not published")} />
                <CopyRow label={t("Database")} value={db.data.database} />
                <CopyRow label={t("User")} value={db.data.username} />
                <CopyRow label={t("Password")} value={t("<Database tab → Credentials>")} mono={false} />
                {db.data.hostPort ? <CopyRow label="URL" value={jdbc} /> : null}
              </dl>
            )}
          </div>
        </Card>
      )}

      {mailpit && (
        <Card>
          <CardHeader
            title={
              <span className="flex items-center gap-2">
                <Mail className="size-4 text-accent-500" aria-hidden /> {t("Mail")}
              </span>
            }
          />
          <div className="p-5">
            <dl>
              <CopyRow label={t("SMTP (from app)")} value="mailpit:1025" />
              {mailpit.webUiPort ? <CopyRow label={t("Inbox")} value={`http://${host}:${mailpit.webUiPort}`} /> : null}
            </dl>
          </div>
        </Card>
      )}

      {rabbitmq && (
        <Card>
          <CardHeader
            title={
              <span className="flex items-center gap-2">
                <Rabbit className="size-4 text-accent-500" aria-hidden /> RabbitMQ
              </span>
            }
          />
          <div className="p-5">
            <dl>
              <CopyRow label={t("AMQP (from app)")} value="rabbitmq:5672" />
              <CopyRow label={t("Host")} value={host} />
              <CopyRow label={t("Port")} value={rabbitmq.hostPort ? String(rabbitmq.hostPort) : t("not published")} />
              <CopyRow label={t("User")} value={rabbitmq.username ?? ""} />
              <CopyRow label={t("Password")} value={t("<Services tab → Show password>")} mono={false} />
              {rabbitmq.webUiPort ? <CopyRow label={t("Management UI")} value={`http://${host}:${rabbitmq.webUiPort}`} /> : null}
            </dl>
          </div>
        </Card>
      )}

      <p className="flex items-center gap-2 text-xs text-subtle">
        <KeyRound className="size-3.5" aria-hidden /> {t("Nothing here is secret except passwords/tokens, which are never shown on this page.")}
      </p>
    </div>
  );
}

/** Placeholder until the first poll reports the server's operation (the request is already running). */
/** package.json snippets that start the inspector for the common setups. */
function nodeDebugExamples(port: number): string {
  return [
    `"dev": "NODE_OPTIONS='--inspect=0.0.0.0:${port}' next dev"        // Next.js (server code listens on ${port + 1})`,
    `"dev": "node --inspect=0.0.0.0:${port} node_modules/vite/bin/vite.js"   // Vite`,
    `"dev": "NODE_OPTIONS='--inspect=0.0.0.0:${port}' nuxt dev"        // Nuxt`,
    `"start": "node --inspect=0.0.0.0:${port} server.js"              // plain Node`,
  ].join("\n");
}

/** Command lines that start debugpy in front of the usual servers. */
function pythonDebugExamples(port: number): string {
  return [
    `python -m debugpy --listen 0.0.0.0:${port} manage.py runserver 0.0.0.0:8000   # Django`,
    `python -m debugpy --listen 0.0.0.0:${port} -m flask --app app:app run --host 0.0.0.0 --port 5000   # Flask`,
    `python -m debugpy --listen 0.0.0.0:${port} -m uvicorn main:app --host 0.0.0.0 --port 8000   # FastAPI / ASGI`,
    `import debugpy; debugpy.listen(("0.0.0.0", ${port}))   # in code, e.g. at the top of manage.py or main.py`,
  ].join("\n");
}

function pendingUpdate(p: Project): Operation {
  const now = new Date().toISOString();
  return { id: "pending", projectId: p.id, projectSlug: p.slug, projectName: p.name, action: "update", startedAt: now, updatedAt: now };
}
