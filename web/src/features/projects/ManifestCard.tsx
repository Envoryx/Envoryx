import { Check, Copy, Download, FileCode2, Save, Wand2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { keys, useProjectManifest } from "@/api/hooks";
import type { ManifestChange, ManifestPlan, Project } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Checkbox, Code, Spinner } from "@/components/ui";
import { copyText } from "@/lib/clipboard";
import { errorText, translateMessage } from "@/lib/errors";

/** Product names stay as they are; the rest of the sections is translated. */
function sectionLabel(section: string, t: TFunction): string {
  const names: Record<string, string> = {
    php: "PHP",
    node: "Node.js",
    python: "Python",
    redis: "Redis",
    memcached: "Memcached",
    mailpit: "Mailpit",
    rabbitmq: "RabbitMQ",
    meilisearch: "Meilisearch",
    typesense: "Typesense",
    opensearch: "OpenSearch",
    ollama: "Ollama",
  };
  if (names[section]) return names[section];
  switch (section) {
    case "docroot":
      return t("Document root");
    case "web":
      return t("Web server");
    case "database":
      return t("Database");
    case "storage":
      return t("Object storage");
    case "env":
      return t("Environment variable");
    case "domain":
      return t("Domain");
    case "worker":
      return t("Worker");
    case "cron":
      return t("Cron job");
    default:
      return section;
  }
}

const marks: Record<ManifestChange["action"], { sign: string; tone: "green" | "amber" | "red" }> = {
  add: { sign: "+", tone: "green" },
  change: { sign: "~", tone: "amber" },
  remove: { sign: "−", tone: "red" },
};

export function ManifestChanges({ plan }: { plan: ManifestPlan }) {
  const { t } = useTranslation();
  return (
    <ul className="divide-y divide-[var(--border)] rounded-md border border-default text-sm">
      {plan.changes.map((c, i) => (
        <li key={i} className={`flex flex-wrap items-baseline gap-x-3 gap-y-1 px-3 py-2 ${c.skipped ? "opacity-60" : ""}`}>
          <Badge tone={marks[c.action].tone} className="w-6 justify-center font-mono">
            {marks[c.action].sign}
          </Badge>
          <span className="font-medium text-fg">
            {sectionLabel(c.section, t)}
            {c.item && <Code className="ml-1.5">{c.item}</Code>}
          </span>
          <span className="min-w-0 flex-1 break-words font-mono text-xs text-muted">
            {c.from && c.to ? `${c.from} → ${c.to}` : c.action === "remove" ? c.from : c.to}
          </span>
          {c.skipped === "prune" && <span className="text-xs text-subtle">{t("kept – removing needs the option below")}</span>}
          {c.skipped === "downgrade" && <span className="text-xs text-subtle">{t("not changed – the data format does not go back to an older version")}</span>}
          {c.skipped === "external" && <span className="text-xs text-subtle">{t("not changed – an external connection needs its password, which the file never holds; set it up in Envoryx")}</span>}
        </li>
      ))}
    </ul>
  );
}

function download(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "application/yaml" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

/**
 * The project manifest (envoryx.yml): the project as a file for the repository, and the
 * file in the project directory compared with the project – after a pull it may describe
 * something else, which "Apply" brings over.
 */
export function ManifestCard({ project }: { project: Project }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const manifest = useProjectManifest(project.id);
  const [busy, setBusy] = useState<"write" | "apply" | null>(null);
  const [prune, setPrune] = useState(false);
  const [copied, setCopied] = useState(false);
  const [showFile, setShowFile] = useState(false);
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);

  const refresh = () => void qc.invalidateQueries({ queryKey: keys.project(project.id) });

  const write = async () => {
    setMsg(null);
    setBusy("write");
    try {
      await api.manifest.write(project.id);
      setMsg({ tone: "green", text: t("envoryx.yml saved in the project directory. Commit it with the code.") });
      refresh();
    } catch (err) {
      setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) });
    } finally {
      setBusy(null);
    }
  };

  const apply = async () => {
    setMsg(null);
    setBusy("apply");
    try {
      const res = await api.manifest.apply(project.id, { prune, start: project.desiredState === "running" });
      setPrune(false);
      setMsg({ tone: "green", text: res.plan.missingSecrets.length ? t("Applied. Still without a value: {{keys}}", { keys: res.plan.missingSecrets.join(", ") }) : t("Applied.") });
      refresh();
    } catch (err) {
      setMsg({ tone: "red", text: errorText(err, t, t("Applying failed")) });
      refresh();
    } finally {
      setBusy(null);
    }
  };

  const data = manifest.data;
  const repo = data?.repository;
  const plan = repo?.plan;
  const pending = plan?.changes.some((c) => !c.skipped) ?? false;
  const destructive = prune && (plan?.changes.some((c) => c.skipped === "prune") ?? false);

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <FileCode2 className="size-4 text-accent-500" aria-hidden /> {t("Project manifest")} <Code>envoryx.yml</Code>
          </span>
        }
        description={t("Runtimes, services, domains, environment, workers and cron jobs as a file in the repository. With it, “envoryx up” in a fresh clone brings up exactly this project.")}
        actions={
          data ? (
            <>
              <Button
                size="sm"
                variant="ghost"
                onClick={async () => {
                  setCopied(await copyText(data.yaml));
                  setTimeout(() => setCopied(false), 1500);
                }}
                icon={copied ? <Check className="size-3.5 text-emerald-500" /> : <Copy className="size-3.5" />}
              >
                {t("Copy")}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => download(data.fileName, data.yaml)} icon={<Download className="size-3.5" />}>
                {t("Download")}
              </Button>
            </>
          ) : undefined
        }
      />
      <div className="space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        {manifest.isPending ? (
          <Spinner />
        ) : manifest.isError ? (
          <Alert tone="red">{errorText(manifest.error, t)}</Alert>
        ) : !repo?.present ? (
          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-sm text-muted">{t("The project directory has no envoryx.yml yet.")}</p>
            <Button onClick={() => void write()} loading={busy === "write"} disabled={busy !== null} icon={<Save className="size-4" />}>
              {t("Save to project directory")}
            </Button>
          </div>
        ) : repo.error ? (
          <Alert tone="red" title={t("The envoryx.yml in the project directory cannot be used")}>
            {translateMessage(repo.error, t)}
          </Alert>
        ) : plan?.inSync ? (
          <div className="flex flex-wrap items-center gap-3">
            <Badge tone="green">{t("matches the project")}</Badge>
            <span className="text-sm text-muted">{t("The envoryx.yml in the project directory describes this project as it is.")}</span>
          </div>
        ) : plan ? (
          <div className="space-y-3">
            <div className="flex flex-wrap items-center gap-3">
              <Badge tone="amber">{t("differs from the project")}</Badge>
              <span className="text-sm text-muted">{t("Applying the envoryx.yml in the project directory changes:")}</span>
            </div>
            <ManifestChanges plan={plan} />
            {plan.changes.some((c) => c.skipped === "prune") && (
              <Checkbox
                checked={prune}
                onChange={(e) => setPrune(e.target.checked)}
                label={t("Also remove what the manifest no longer has")}
                description={t("Removing a database or a service with a volume deletes its data.")}
              />
            )}
            <div className="flex flex-wrap gap-2">
              {(pending || destructive) && (
                <Button variant={destructive ? "danger" : "primary"} onClick={() => void apply()} loading={busy === "apply"} disabled={busy !== null} icon={<Wand2 className="size-4" />}>
                  {t("Apply to project")}
                </Button>
              )}
              <Button onClick={() => void write()} loading={busy === "write"} disabled={busy !== null} icon={<Save className="size-4" />}>
                {t("Overwrite the file with the project")}
              </Button>
            </div>
          </div>
        ) : null}
        {data && (
          <div>
            <button type="button" className="text-xs font-medium text-accent-600 hover:underline" onClick={() => setShowFile((v) => !v)} aria-expanded={showFile}>
              {showFile ? t("Hide the project as envoryx.yml") : t("Show the project as envoryx.yml")}
            </button>
            {showFile && <pre className="mt-2 max-h-96 overflow-auto rounded-md bg-[#0f1115] p-3 font-mono text-[11px] leading-5 text-zinc-200">{data.yaml}</pre>}
          </div>
        )}
      </div>
    </Card>
  );
}
