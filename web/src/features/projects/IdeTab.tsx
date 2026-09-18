import { Bug, Database, KeyRound, Mail, MonitorSmartphone, TerminalSquare } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router-dom";
import { useMutation } from "@tanstack/react-query";
import { api, ApiError } from "@/api/client";
import { useDatabaseInfo, useExtraServices, useSettings, useUpdateProject } from "@/api/hooks";
import type { PHPConfig, Project } from "@/api/types";
import { Alert, Button, Card, CardHeader, Checkbox, Code } from "@/components/ui";
import { CopyButton, CopyRow } from "./DatabaseTab";

/** Everything an IDE needs, ready to copy: Xdebug server, SSH interpreter, database, mail. */
export function IdeTab({ project: p }: { project: Project }) {
  const settings = useSettings();
  const hasDb = p.services.some((s) => s.kind === "database" && s.enabled);
  const db = useDatabaseInfo(p.id, hasDb);
  const extras = useExtraServices(p.id);
  const s = settings.data;
  const host = s?.publicHost || window.location.hostname;
  const projectsHost = s?.hostPath ? (s.hostPath.overrides[s.projectsDir] ?? s.hostPath.detected[s.projectsDir]) : undefined;
  const hostDir = projectsHost ? `${projectsHost}/${p.path}` : "<projects share>/" + p.path;
  const php = p.services.find((x) => x.kind === "php" && x.enabled);
  const phpCfg = (php?.config ?? {}) as unknown as Partial<PHPConfig>;
  const hostname = p.hostnames[0] ?? `${p.slug}.test`;
  const ssh = s?.ssh;
  const sshHost = s?.proxy?.address || host;
  const mailpit = extras.data?.find((e) => e.kind === "mailpit");
  const update = useUpdateProject(p.id);
  const [gwMsg, setGwMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const stopBackend = useMutation({
    mutationFn: () => api.projects.stopIDEBackend(p.id),
    onSuccess: (r) => setGwMsg({ tone: "green", text: r.stopped > 0 ? "IDE backend stopped." : "No IDE backend was running." }),
    onError: (err) => setGwMsg({ tone: "red", text: err instanceof ApiError ? err.message : "Request failed" }),
  });

  const phpXml = `<?xml version="1.0" encoding="UTF-8"?>
<project version="4">
  <component name="PhpProjectServersManager">
    <servers>
      <server host="${hostname}" id="staqio-${p.slug}" name="${hostname}" use_path_mappings="true">
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
      <Card>
        <CardHeader
          title={
            <span className="flex items-center gap-2">
              <TerminalSquare className="size-4 text-accent-500" aria-hidden /> Remote interpreter (SSH)
            </span>
          }
          description="Run PHP, Composer, PHPUnit and Artisan inside the project container from your IDE. PhpStorm: Settings → PHP → CLI Interpreter → “…” → From Docker, Vagrant, VM, WSL, Remote… → SSH. VS Code: Remote-SSH. Plain terminal: ssh."
        />
        <div className="p-5">
          {!ssh?.enabled ? (
            <Alert tone="amber">The SSH server is disabled (<Code>STAQIO_SSH</Code> is empty).</Alert>
          ) : ssh.port === 0 ? (
            <Alert tone="amber">The SSH port 2222 is not published on the host – add a port mapping <Code>2222:2222</Code> to the Staqio container.</Alert>
          ) : (
            <dl>
              <CopyRow label="Host" value={sshHost} />
              <CopyRow label="Port" value={String(ssh.port)} />
              <CopyRow label="User (PHP)" value={p.slug} />
              {p.services.some((x) => x.kind === "node" && x.enabled) && <CopyRow label="User (Node)" value={`${p.slug}.node`} />}
              <CopyRow label="Password" value="<API token from Settings → API tokens>" mono={false} />
              <CopyRow label="PHP path" value="/usr/local/bin/php" />
              <CopyRow label="Project path" value="/var/www/html" />
              <CopyRow label="Helpers path" value="/home/staqio/.phpstorm_helpers" />
              <CopyRow label="Host key" value={ssh.fingerprint} />
              <CopyRow label="ssh" value={`ssh -p ${ssh.port} ${p.slug}@${sshHost}`} />
            </dl>
          )}
          <p className="mt-3 text-xs text-subtle">
            Authentication: an <Link to="/settings" className="underline">API token</Link> as password, or your public key under Settings → SSH access. Sessions run as the project owner inside <Code>staqio-{p.slug}-php</Code>; the project must be running.
          </p>
        </div>
      </Card>

      <Card>
        <CardHeader
          title={
            <span className="flex items-center gap-2">
              <MonitorSmartphone className="size-4 text-accent-500" aria-hidden /> JetBrains Gateway (optional)
            </span>
          }
          description="Run the full PhpStorm/WebStorm backend inside the project container and work with the thin client. Needs a capable server: 2–4 GB RAM and CPU per open project. Nothing runs until you connect."
        />
        <div className="space-y-3 p-5">
          {gwMsg && <Alert tone={gwMsg.tone}>{gwMsg.text}</Alert>}
          <Checkbox
            label="Allow JetBrains Gateway for this project"
            description="Enables SSH port forwarding into the container and mounts a shared IDE backend cache (/config/jetbrains, downloaded once for all projects). Recreates the PHP/Node container."
            checked={!!p.ideGateway}
            disabled={update.isPending}
            onChange={(e) => {
              setGwMsg(null);
              update.mutate({ ideGateway: e.target.checked }, { onError: (err) => setGwMsg({ tone: "red", text: err instanceof ApiError ? err.message : "Saving failed" }) });
            }}
          />
          {p.ideGateway && ssh?.enabled && ssh.port > 0 && (
            <>
              <ol className="list-decimal space-y-1 pl-5 text-sm text-muted">
                <li>Gateway → <span className="text-fg">SSH → New connection</span>: host <Code>{sshHost}</Code>, port <Code>{ssh.port}</Code>, user <Code>{p.slug}</Code>, password = API token (or key).</li>
                <li>IDE: PhpStorm (or WebStorm with user <Code>{p.slug}.node</Code>); project directory <Code>/var/www/html</Code>.</li>
                <li>Gateway installs the backend into <Code>/home/staqio/.cache/JetBrains</Code> (shared cache) and opens the thin client.</li>
              </ol>
              <p className="text-xs text-subtle">Close the project in Gateway when you are done, or stop the backend here to free memory on the server.</p>
              <Button size="sm" onClick={() => { setGwMsg(null); stopBackend.mutate(); }} loading={stopBackend.isPending}>
                Stop IDE backend
              </Button>
            </>
          )}
        </div>
      </Card>

      <Card>
        <CardHeader
          title={
            <span className="flex items-center gap-2">
              <Bug className="size-4 text-accent-500" aria-hidden /> Xdebug
            </span>
          }
          description={phpCfg.xdebug ? `Enabled (${phpCfg.xdebugMode ?? "always"}). PhpStorm: Settings → PHP → Servers.` : "Not enabled – switch it on in the Runtime tab. Values below apply once enabled."}
        />
        <div className="p-5">
          <dl>
            <CopyRow label="Server name" value={hostname} />
            <CopyRow label="Server host" value={hostname} />
            <CopyRow label="Debug port" value="9003" />
            <CopyRow label="IDE key" value={phpCfg.xdebugIdeKey || "PHPSTORM"} />
            <CopyRow label="Path mapping" value={`${hostDir} → /var/www/html`} />
          </dl>
          <div className="mt-3">
            <div className="flex items-center justify-between">
              <p className="text-xs text-muted"><Code>.idea/php.xml</Code> (server + mapping; adjust <Code>$PROJECT_DIR$</Code> if the IDE project is not the project root):</p>
              <CopyButton value={phpXml} label="php.xml" />
            </div>
            <pre className="mt-1 overflow-x-auto rounded-md bg-muted p-2 font-mono text-[11px]">{phpXml}</pre>
          </div>
        </div>
      </Card>

      {hasDb && (
        <Card>
          <CardHeader
            title={
              <span className="flex items-center gap-2">
                <Database className="size-4 text-accent-500" aria-hidden /> Database tool / DataGrip
              </span>
            }
            description={db.data?.hostPort ? "Connect from your machine through the published port." : "Publish the database port in the Database tab to connect from your machine."}
          />
          <div className="p-5">
            {db.data && (
              <dl>
                <CopyRow label="Type" value={db.data.type} mono={false} />
                <CopyRow label="Host" value={host} />
                <CopyRow label="Port" value={db.data.hostPort ? String(db.data.hostPort) : "not published"} />
                <CopyRow label="Database" value={db.data.database} />
                <CopyRow label="User" value={db.data.username} />
                <CopyRow label="Password" value="<Database tab → Credentials>" mono={false} />
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
                <Mail className="size-4 text-accent-500" aria-hidden /> Mail
              </span>
            }
          />
          <div className="p-5">
            <dl>
              <CopyRow label="SMTP (from app)" value="mailpit:1025" />
              {mailpit.webUiPort ? <CopyRow label="Inbox" value={`http://${host}:${mailpit.webUiPort}`} /> : null}
            </dl>
          </div>
        </Card>
      )}

      <p className="flex items-center gap-2 text-xs text-subtle">
        <KeyRound className="size-3.5" aria-hidden /> Nothing here is secret except passwords/tokens, which are never shown on this page.
      </p>
    </div>
  );
}
