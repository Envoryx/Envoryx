import { ExternalLink, Play, RotateCw, Square, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { ApiError } from "@/api/client";
import { useDeleteProject, useProjectAction, useProjectLinks, type ProjectAction } from "@/api/hooks";
import type { Project } from "@/api/types";
import { Button, Checkbox, Dialog, Field, Input, Alert } from "@/components/ui";

export function useActionError() {
  const { t } = useTranslation();
  const [error, setError] = useState<string | null>(null);
  const capture = (err: unknown) => setError(err instanceof ApiError ? err.message : t("Request failed"));
  return { error, setError, capture };
}

/** Start / stop / restart / open buttons shared by list and detail views. */
export function ProjectActionButtons({
  project,
  size = "sm",
  onError,
}: {
  project: Project;
  size?: "sm" | "md";
  onError?: (err: unknown) => void;
}) {
  const { t } = useTranslation();
  const action = useProjectAction();
  const links = useProjectLinks();
  const state = project.status.state;
  const busy = action.isPending && action.variables?.id === project.id;
  const transitional = state === "creating" || state === "deleting";
  const run = (a: ProjectAction) =>
    action.mutate({ id: project.id, action: a }, { onError: (err) => onError?.(err) });
  const running = state === "running";
  const { url } = links(project);
  const pending = (a: ProjectAction) => busy && action.variables?.action === a;

  return (
    <div className="flex items-center gap-1.5">
      {running ? (
        <Button size={size} onClick={() => run("stop")} loading={pending("stop")} disabled={busy || transitional} icon={<Square className="size-3.5" />} title={t("Stop")}>
          {t("Stop")}
        </Button>
      ) : (
        <Button size={size} variant="primary" onClick={() => run("start")} loading={pending("start")} disabled={busy || transitional} icon={<Play className="size-3.5" />} title={t("Start")}>
          {t("Start")}
        </Button>
      )}
      <Button size={size} onClick={() => run("restart")} loading={pending("restart")} disabled={busy || transitional} icon={<RotateCw className="size-3.5" />} title={t("Restart – also pulls updated runtime images")}>
        {t("Restart")}
      </Button>
      {url && (
        <a
          href={url}
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex h-8 items-center gap-1.5 rounded-md border border-default bg-elevated px-2.5 text-xs font-medium text-fg hover:bg-muted aria-disabled:opacity-50"
          aria-disabled={!running}
          title={running ? t("Open {{url}}", { url }) : t("Project is not running")}
        >
          <ExternalLink className="size-3.5" aria-hidden />
          {t("Open")}
        </a>
      )}
    </div>
  );
}

export function DeleteProjectDialog({ project, open, onClose }: { project: Project; open: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const del = useDeleteProject();
  const navigate = useNavigate();
  const [confirm, setConfirm] = useState("");
  const [deleteFiles, setDeleteFiles] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const close = () => {
    setConfirm("");
    setDeleteFiles(false);
    setError(null);
    onClose();
  };

  const submit = () => {
    setError(null);
    del.mutate(
      { id: project.id, confirm, deleteFiles },
      {
        onSuccess: () => {
          close();
          navigate("/projects");
        },
        onError: (err) => setError(err instanceof ApiError ? err.message : t("Delete failed")),
      },
    );
  };

  return (
    <Dialog
      open={open}
      onClose={close}
      title={t("Delete “{{name}}”?", { name: project.name })}
      description={t("This stops and removes all containers, the network and generated configuration of this project.")}
      footer={
        <>
          <Button onClick={close} disabled={del.isPending}>
            {t("Cancel")}
          </Button>
          <Button variant="danger" onClick={submit} loading={del.isPending} disabled={confirm !== project.slug} icon={<Trash2 className="size-4" />}>
            {t("Delete project")}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {error && <Alert tone="red">{error}</Alert>}
        <Field label={t("Type {{slug}} to confirm", { slug: project.slug })} htmlFor="confirm-slug">
          <Input id="confirm-slug" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="off" spellCheck={false} />
        </Field>
        <Checkbox
          label={t("Also delete project files")}
          description={t("Permanently removes /projects/{{path}}. This cannot be undone.", { path: project.path })}
          checked={deleteFiles}
          onChange={(e) => setDeleteFiles(e.target.checked)}
        />
      </div>
    </Dialog>
  );
}
