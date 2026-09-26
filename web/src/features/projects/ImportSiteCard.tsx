import { useState } from "react";
import { useTranslation } from "react-i18next";
import { FileArchive, RotateCcw, Upload } from "lucide-react";
import { api } from "@/api/client";
import type { SiteImport } from "@/api/types";
import { Alert, Button, Checkbox, Code, Field, Input } from "@/components/ui";
import { errorText } from "@/lib/errors";
import { formatBytes } from "@/lib/format";

const runtimeNames: Record<string, string> = { php: "PHP", static: "Static site", node: "Node.js", python: "Python", go: "Go", ruby: "Ruby" };
const databaseNames: Record<string, string> = { mariadb: "MariaDB", mysql: "MySQL", postgresql: "PostgreSQL" };
const webNames: Record<string, string> = { apache: "Apache", caddy: "Caddy", nginx: "nginx" };

/**
 * The "existing website" source of the project wizard: uploads the site's files (and a
 * database dump), shows what Envoryx recognised and whether it adapts the configuration.
 */
export function ImportSiteCard({
  value,
  onUploaded,
  onDiscard,
  adaptConfig,
  onAdaptConfig,
}: {
  value: SiteImport | null;
  onUploaded: (imp: SiteImport) => void;
  onDiscard: () => void;
  adaptConfig: boolean;
  onAdaptConfig: (on: boolean) => void;
}) {
  const { t } = useTranslation();
  const [site, setSite] = useState<File | null>(null);
  const [dump, setDump] = useState<File | null>(null);
  const [progress, setProgress] = useState<{ loaded: number; total: number } | null>(null);
  const [error, setError] = useState<string | null>(null);

  const upload = () => {
    if (!site) return;
    setError(null);
    setProgress({ loaded: 0, total: site.size + (dump?.size ?? 0) });
    api.siteImports
      .upload(site, dump, (loaded, total) => setProgress({ loaded, total }))
      .then((r) => {
        setProgress(null);
        onUploaded(r.import);
      })
      .catch((err: unknown) => {
        setProgress(null);
        setError(errorText(err, t, t("Upload failed")));
      });
  };

  if (!value) {
    const busy = progress !== null;
    const percent = progress && progress.total > 0 ? Math.min(100, Math.round((progress.loaded / progress.total) * 100)) : 0;
    return (
      <div className="space-y-4 rounded-md border border-default p-4">
        <p className="text-sm text-muted">{t("Upload the files of the site as the old host or your FTP client packs them. Envoryx recognises WordPress, Laravel, Symfony, Drupal, TYPO3, Joomla and plain PHP or HTML sites and suggests the settings of the next steps.")}</p>
        <Field label={t("Website archive")} htmlFor="import-site" hint={t("ZIP or tar.gz of the site's files. A single folder around them (public_html, httpdocs…) is left out.")}>
          <Input id="import-site" type="file" accept=".zip,.tar,.tar.gz,.tgz,application/zip,application/gzip,application/x-tar" disabled={busy} onChange={(e) => setSite(e.target.files?.[0] ?? null)} />
        </Field>
        <Field label={t("Database dump (optional)")} htmlFor="import-dump" hint={t(".sql or .sql.gz, e.g. an export from phpMyAdmin, mysqldump or pg_dump. It is imported into the project database.")}>
          <Input id="import-dump" type="file" accept=".sql,.gz,application/sql,application/gzip" disabled={busy} onChange={(e) => setDump(e.target.files?.[0] ?? null)} />
        </Field>
        {busy && (
          <div className="space-y-1" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent} aria-label={t("Upload progress")}>
            <div className="h-1.5 overflow-hidden rounded-full bg-muted">
              <div className="h-full bg-accent-600 transition-[width]" style={{ width: `${percent}%` }} />
            </div>
            <p className="text-xs text-subtle">
              {percent < 100
                ? t("Uploading {{done}} of {{total}}", { done: formatBytes(progress.loaded), total: formatBytes(progress.total) })
                : t("Analysing the website…")}
            </p>
          </div>
        )}
        {error && (
          <Alert tone="red" title={t("The website could not be read")}>
            {error}
          </Alert>
        )}
        <Button variant="primary" onClick={upload} disabled={!site || busy} loading={busy} icon={<Upload className="size-4" />}>
          {t("Upload and analyse")}
        </Button>
      </div>
    );
  }

  const a = value.analysis;
  const framework = [t(a.framework.name), a.framework.version].filter(Boolean).join(" ");
  const cfg = a.config;
  return (
    <div className="space-y-4 rounded-md border border-default p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="flex items-start gap-3">
          <FileArchive className="mt-0.5 size-5 shrink-0 text-accent-600" />
          <div>
            <p className="text-sm font-medium text-fg">{t("Recognised: {{framework}}", { framework })}</p>
            <p className="text-xs text-subtle">
              {value.siteName} · {t("{{count}} files", { count: a.files })} · {formatBytes(a.bytes)}
              {value.dumpName && (
                <>
                  {" · "}
                  {value.dumpName}
                  {a.dump?.tool && ` (${a.dump.tool}${a.dump.server ? ` ${a.dump.server}` : ""})`}
                </>
              )}
            </p>
          </div>
        </div>
        <Button variant="ghost" onClick={onDiscard} icon={<RotateCcw className="size-4" />}>
          {t("Other files")}
        </Button>
      </div>

      <dl className="grid gap-x-6 gap-y-1.5 text-sm sm:grid-cols-[10rem_1fr]">
        <dt className="text-muted">{t("Runtime")}</dt>
        <dd>
          {t(runtimeNames[a.runtime] ?? a.runtime)}
          {a.phpVersion && ` ${a.phpVersion}`}
        </dd>
        {a.phpExtensions && a.phpExtensions.length > 0 && (
          <>
            <dt className="text-muted">{t("PHP extensions")}</dt>
            <dd className="font-mono text-xs">{a.phpExtensions.join(", ")}</dd>
          </>
        )}
        <dt className="text-muted">{t("Document root")}</dt>
        <dd className="font-mono text-xs">{a.docroot || t("(project root)")}</dd>
        {a.web && (
          <>
            <dt className="text-muted">{t("Web server")}</dt>
            <dd>{webNames[a.web] ?? a.web}</dd>
          </>
        )}
        <dt className="text-muted">{t("Database")}</dt>
        <dd>{a.database ? (databaseNames[a.database] ?? a.database) : t("None")}</dd>
      </dl>
      <p className="text-xs text-subtle">{t("These values are filled into the next steps; change them there if needed.")}</p>

      {cfg?.mode === "adapt" && (
        <Checkbox
          label={t("Adapt the configuration to Envoryx")}
          description={t("Envoryx points {{file}} at the project database and lets the site answer on its new address. The original is kept next to it with .envoryx-original in its name.", { file: cfg.path })}
          checked={adaptConfig}
          onChange={(e) => onAdaptConfig(e.target.checked)}
        />
      )}
      {cfg?.mode === "env" && (
        <p className="text-sm text-muted">
          {t("The DB_* variables and DATABASE_URL Envoryx injects override the values in {{file}}; nothing has to be changed.", { file: cfg.path })}
          {(a.framework.id === "laravel" || a.framework.id === "symfony" || a.framework.id === "shopware") && (
            <span className="mt-2 block">
              <Checkbox label={t("Clear the cached configuration")} description={t("Removes the configuration cache the old server left behind (bootstrap/cache/config.php, var/cache).")} checked={adaptConfig} onChange={(e) => onAdaptConfig(e.target.checked)} />
            </span>
          )}
        </p>
      )}
      {(cfg?.mode === "manual" || (!cfg && a.database)) && (
        <Alert tone="blue" title={t("Connect the site to the project database")}>
          {cfg ? t("Change the database connection in {{file}} after creation.", { file: cfg.path }) : t("Change the database connection of the site after creation.")}{" "}
          {t("The server is reached at the host “database”; name, user and password are on the project's Database tab (also injected as DB_DATABASE, DB_USERNAME and DB_PASSWORD).")}
          {a.configCandidates && a.configCandidates.length > 1 && (
            <span className="mt-1 block">
              {t("Files that open a connection:")}{" "}
              {a.configCandidates.map((c, i) => (
                <span key={c}>
                  {i > 0 && ", "}
                  <Code>{c}</Code>
                </span>
              ))}
            </span>
          )}
        </Alert>
      )}

      {a.notices.length > 0 && (
        <ul className="space-y-1.5">
          {a.notices.map((n, i) => (
            <li key={i} className={n.level === "warning" ? "text-sm text-amber-700 dark:text-amber-400" : "text-sm text-muted"}>
              {t(n.text, n.params ?? {})}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** A project name from the archive's file name: "mysite_backup.tar.gz" → "mysite backup". */
export function nameFromArchive(file: string): string {
  return file
    .replace(/\.(zip|tgz|tar\.gz|tar)$/i, "")
    .replace(/[-_.]+/g, " ")
    .trim()
    .slice(0, 60);
}
