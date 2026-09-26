import { Download, ExternalLink, Trash2, X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { useOllamaModels, useOllamaMutations } from "@/api/hooks";
import type { OllamaModel, OllamaPull, Project } from "@/api/types";
import { Alert, Badge, Button, Dialog, ErrorState, Field, Input, Spinner } from "@/components/ui";
import { formatBytes } from "@/lib/format";
import { errorText } from "@/lib/errors";

type Message = { tone: "green" | "red"; text: string };

/** Mirrors ValidateOllamaModel in internal/project: name[:tag], optionally below a namespace or registry host. */
const MODEL_RE = /^[A-Za-z0-9][A-Za-z0-9._-]*(\/[A-Za-z0-9][A-Za-z0-9._-]*)*(:[A-Za-z0-9][A-Za-z0-9._-]*)?$/;

/** One download: a bar while it runs, the outcome once it ended. */
function PullRow({ pull, onCancel, cancelling }: { pull: OllamaPull; onCancel: () => void; cancelling: boolean }) {
  const { t } = useTranslation();
  const percent = pull.total > 0 ? Math.min(100, Math.round((pull.completed / pull.total) * 100)) : 0;
  if (pull.done) {
    if (pull.status === "success") return null; // the model is in the list now
    return (
      <Alert tone={pull.status === "failed" ? "red" : "amber"}>
        {pull.status === "failed" ? t("Downloading {{model}} failed: {{error}}", { model: pull.model, error: pull.error ?? "" }) : t("Download of {{model}} cancelled. The next download of it continues where this one stopped.", { model: pull.model })}
      </Alert>
    );
  }
  return (
    <div className="space-y-1 rounded-md border border-default px-3 py-2" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent} aria-label={t("Download of {{model}}", { model: pull.model })}>
      <div className="flex items-center justify-between gap-2">
        <span className="font-mono text-xs text-fg">{pull.model}</span>
        <Button variant="ghost" size="sm" icon={<X className="size-3.5" />} loading={cancelling} onClick={onCancel}>
          {t("Cancel")}
        </Button>
      </div>
      <div className="h-1.5 overflow-hidden rounded-full bg-muted">
        <div className="h-full bg-accent-600 transition-[width]" style={{ width: `${percent}%` }} />
      </div>
      <p className="text-xs text-subtle">
        {pull.total > 0 ? t("{{done}} of {{total}}", { done: formatBytes(pull.completed), total: formatBytes(pull.total) }) : pull.status}
      </p>
    </div>
  );
}

/**
 * The models of the Ollama store every project shares: download one by name, watch it
 * arrive, delete it. Needs Ollama running – the list comes from Ollama itself.
 */
export function OllamaModels({ project, running, onMessage }: { project: Project; running: boolean; onMessage: (m: Message) => void }) {
  const { t } = useTranslation();
  const models = useOllamaModels(project.id, running);
  const { pull, cancel, remove } = useOllamaMutations(project.id);
  const [name, setName] = useState("");
  const [deleteTarget, setDeleteTarget] = useState<OllamaModel | null>(null);
  const fail = (err: unknown, fallback: string) => onMessage({ tone: "red", text: errorText(err, t, fallback) });
  const trimmed = name.trim();
  const valid = trimmed === "" || MODEL_RE.test(trimmed);

  if (!running) return <p className="text-sm text-muted">{t("Start the project to download and manage models.")}</p>;

  return (
    <div className="space-y-3">
      <div>
        <h3 className="text-sm font-medium text-fg">{t("Models")}</h3>
        <p className="text-xs text-subtle">{t("One store for all projects: a model is downloaded once, whichever project uses it.")}</p>
      </div>

      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (!trimmed || !valid) return;
          pull.mutate(trimmed, { onSuccess: () => setName(""), onError: (err) => fail(err, t("Starting the download failed")) });
        }}
      >
        <Field label={t("Model")} htmlFor={`ollama-model-${project.id}`} error={valid ? undefined : t("Not a model name, e.g. llama3.2 or qwen3:8b")}>
          <Input id={`ollama-model-${project.id}`} value={name} onChange={(e) => setName(e.target.value)} placeholder="llama3.2, qwen3:8b, nomic-embed-text …" autoComplete="off" spellCheck={false} maxLength={200} />
        </Field>
        <Button type="submit" variant="primary" icon={<Download className="size-4" />} disabled={!trimmed || !valid} loading={pull.isPending}>
          {t("Download")}
        </Button>
      </form>
      <a href="https://ollama.com/library" target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-xs text-accent-600 hover:underline dark:text-accent-300">
        {t("Browse the Ollama library")} <ExternalLink className="size-3" />
      </a>

      {models.isPending ? (
        <Spinner />
      ) : models.isError ? (
        <ErrorState message={errorText(models.error, t)} />
      ) : (
        <>
          {models.data.pulls.map((p) => (
            <PullRow key={`${p.model}-${p.startedAt}`} pull={p} cancelling={cancel.isPending && cancel.variables === p.model} onCancel={() => cancel.mutate(p.model, { onError: (err) => fail(err, t("Cancelling failed")) })} />
          ))}
          {models.data.models.length === 0 ? (
            <p className="py-2 text-sm text-muted">{t("No models yet.")}</p>
          ) : (
            <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
              {models.data.models.map((m) => (
                <li key={m.name} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-3 py-2">
                  <div className="min-w-[10rem] flex-1">
                    <p className="font-mono text-sm text-fg">{m.name}</p>
                    <p className="mt-0.5 flex flex-wrap gap-1.5 text-xs text-subtle">
                      {m.parameterSize && <Badge tone="blue">{m.parameterSize}</Badge>}
                      {m.quantization && <Badge>{m.quantization}</Badge>}
                      {m.family && <span>{m.family}</span>}
                    </p>
                  </div>
                  <span className="text-xs tabular-nums text-muted">{formatBytes(m.size)}</span>
                  <Button variant="ghost" size="sm" aria-label={t("Delete {{model}}", { model: m.name })} onClick={() => setDeleteTarget(m)}>
                    <Trash2 className="size-4" />
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </>
      )}

      <Dialog
        open={deleteTarget !== null}
        onClose={() => setDeleteTarget(null)}
        title={t("Delete {{model}}?", { model: deleteTarget?.name ?? "" })}
        description={t("The model leaves the store all projects share: every project with Ollama loses it and has to download it again.")}
        footer={
          <>
            <Button onClick={() => setDeleteTarget(null)}>{t("Cancel")}</Button>
            <Button
              variant="danger"
              loading={remove.isPending}
              onClick={() => {
                const target = deleteTarget;
                if (!target) return;
                remove.mutate(target.name, {
                  onSuccess: () => {
                    setDeleteTarget(null);
                    onMessage({ tone: "green", text: t("{{model}} deleted ({{size}} freed).", { model: target.name, size: formatBytes(target.size) }) });
                  },
                  onError: (err) => {
                    setDeleteTarget(null);
                    fail(err, t("Deleting failed"));
                  },
                });
              }}
            >
              {t("Delete")}
            </Button>
          </>
        }
      />
    </div>
  );
}
