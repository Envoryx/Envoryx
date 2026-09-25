import { Copy, ExternalLink, Pencil, Play, RotateCw, Square, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useDeleteProject, useDuplicateProject, useProjectAction, useProjectLinks, useRenameProject, type ProjectAction } from "@/api/hooks";
import type { DuplicateProjectRequest, Project, RenameProjectRequest } from "@/api/types";
import { Button, Checkbox, Dialog, Field, Input, Alert } from "@/components/ui";
import { errorText } from "@/lib/errors";
import { slugify } from "@/lib/format";
import { CreateProgress } from "./CreateProgress";
import { databaseServices } from "./databases";

export function useActionError() {
  const { t } = useTranslation();
  const [error, setError] = useState<string | null>(null);
  const capture = (err: unknown) => setError(errorText(err, t, t("Request failed")));
  return { error, setError, capture };
}

/** Start / stop / restart / open buttons shared by list and detail views. Restart only
 * makes sense for a running project; a stopped one gets Start (which pulls missing images). */
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
  // Another tab or user may be operating on the project: the server reports it.
  const transitional = state === "creating" || state === "deleting" || !!project.status.operation;
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
      {running && (
        <Button size={size} onClick={() => run("restart")} loading={pending("restart")} disabled={busy || transitional} icon={<RotateCw className="size-3.5" />} title={t("Restart – also pulls updated runtime images")}>
          {t("Restart")}
        </Button>
      )}
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
        onError: (err) => setError(errorText(err, t, t("Delete failed"))),
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

/**
 * Copies a project: shop → shop-test in one dialog. Every part defaults to what the
 * original has; the copy keeps the original's database credentials, so a .env that lives
 * in the project files keeps working.
 */
export function DuplicateProjectDialog({ project, open, onClose }: { project: Project; open: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const duplicate = useDuplicateProject();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [path, setPath] = useState("");
  const [pathTouched, setPathTouched] = useState(false);
  const [parts, setParts] = useState({ files: true, includeDependencies: false, database: true, storage: true, workers: true, git: true, start: false });
  const [error, setError] = useState<string | null>(null);

  const has = (kind: string) => project.services.some((s) => s.kind === kind && s.enabled);
  const suggestion = t("{{name}} Test", { name: project.name });
  const wanted = name.trim() || suggestion;
  const slug = slugify(wanted);
  const dir = pathTouched ? path.trim() : slug;

  const close = () => {
    if (duplicate.isPending) return;
    setName("");
    setPath("");
    setPathTouched(false);
    setError(null);
    onClose();
  };

  const submit = () => {
    setError(null);
    const body: DuplicateProjectRequest = {
      name: wanted,
      files: parts.files,
      includeDependencies: parts.files && parts.includeDependencies,
      database: parts.database,
      storage: parts.storage,
      workers: parts.workers,
      git: parts.git,
      start: parts.start,
    };
    // The directory is only sent when it differs from the slug the server derives anyway.
    if (dir !== slug) body.path = dir;
    duplicate.mutate(
      { id: project.id, body },
      {
        onSuccess: (copy) => {
          onClose();
          navigate(`/projects/${copy.id}`);
        },
        onError: (err) => setError(errorText(err, t, t("Copying failed"))),
      },
    );
  };

  return (
    <Dialog
      open={open}
      onClose={close}
      title={t("Duplicate “{{name}}”?", { name: project.name })}
      description={t("The copy gets its own directory, host ports and containers. Extra domains and the backup schedule stay with the original.")}
      footer={
        <>
          <Button onClick={close} disabled={duplicate.isPending}>
            {t("Cancel")}
          </Button>
          <Button variant="primary" onClick={submit} loading={duplicate.isPending} disabled={slug === "" || slug === project.slug} icon={<Copy className="size-4" />}>
            {t("Duplicate project")}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {error && <Alert tone="red">{error}</Alert>}
        <Field
          label={t("Name of the copy")}
          htmlFor="copy-name"
          error={slug === project.slug ? t("The copy needs a name of its own.") : undefined}
          hint={t("Identifier: {{slug}}", { slug: slug || "—" })}
        >
          <Input id="copy-name" value={name} onChange={(e) => setName(e.target.value)} placeholder={suggestion} autoComplete="off" spellCheck={false} />
        </Field>
        <Field label={t("Directory")} htmlFor="copy-path" hint={t("Below the projects directory.")}>
          <Input id="copy-path" value={pathTouched ? path : slug} onChange={(e) => { setPath(e.target.value); setPathTouched(true); }} spellCheck={false} />
        </Field>
        <div className="space-y-2">
          <Checkbox
            label={t("Copy the project files")}
            description={t("Everything in /projects/{{path}}.", { path: project.path })}
            checked={parts.files}
            onChange={(e) => setParts({ ...parts, files: e.target.checked })}
          />
          {parts.files && (
            <div className="pl-6">
              <Checkbox
                label={t("Including dependencies")}
                description={t("vendor/, node_modules/ and the other directories a build recreates. Slower, but the copy runs without installing them again.")}
                checked={parts.includeDependencies}
                onChange={(e) => setParts({ ...parts, includeDependencies: e.target.checked })}
              />
            </div>
          )}
          {databaseServices(project).length > 0 && (
            <Checkbox
              label={t("Copy the database")}
              description={t("The contents are dumped and imported into the copy's own database server. It keeps the credentials of the original.")}
              checked={parts.database}
              onChange={(e) => setParts({ ...parts, database: e.target.checked })}
            />
          )}
          {has("storage") && (
            <Checkbox
              label={t("Copy the objects of the bucket")}
              checked={parts.storage}
              onChange={(e) => setParts({ ...parts, storage: e.target.checked })}
            />
          )}
          <Checkbox
            label={t("Copy the workers and cron jobs")}
            description={t("The queue and scheduler definitions of the original.")}
            checked={parts.workers}
            onChange={(e) => setParts({ ...parts, workers: e.target.checked })}
          />
          {project.git.url !== "" && (
            <Checkbox
              label={t("Copy the repository binding")}
              description={project.git.url}
              checked={parts.git}
              onChange={(e) => setParts({ ...parts, git: e.target.checked })}
            />
          )}
          <Checkbox label={t("Start the copy when it is ready")} checked={parts.start} onChange={(e) => setParts({ ...parts, start: e.target.checked })} />
        </div>
        {duplicate.isPending && <CreateProgress slug={slug} action="duplicate" title={t("Copying the project…")} hint={t("Files and the database are copied here; big projects take a moment.")} />}
      </div>
    </Dialog>
  );
}


/**
 * Renames a project. Everything derived from the identifier moves with it, which means
 * recreated containers and – unless the data names are kept – a renamed database and
 * bucket, so the dialog says so and asks for the current identifier.
 */
export function RenameProjectDialog({ project, open, onClose }: { project: Project; open: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const rename = useRenameProject(project.id);
  const [name, setName] = useState(project.name);
  const [path, setPath] = useState("");
  const [pathTouched, setPathTouched] = useState(false);
  const [keepDataNames, setKeepDataNames] = useState(false);
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);

  const slug = slugify(name);
  // A directory that still matches the identifier follows it; one that was chosen by hand
  // stays where it is until the field is touched.
  const follows = project.path === project.slug;
  const dir = pathTouched ? path.trim() : follows ? slug : project.path;
  const hasDatabase = databaseServices(project).length > 0;
  const hasStorage = project.services.some((s) => s.kind === "storage" && s.enabled);
  const unchanged = name.trim() === project.name && dir === project.path;

  const close = () => {
    if (rename.isPending) return;
    setName(project.name);
    setPath("");
    setPathTouched(false);
    setConfirm("");
    setError(null);
    onClose();
  };

  const submit = () => {
    setError(null);
    const body: RenameProjectRequest = { name: name.trim(), confirm, keepDataNames };
    if (dir !== project.path) body.path = dir;
    rename.mutate(body, {
      onSuccess: () => close(),
      onError: (err) => setError(errorText(err, t, t("Renaming failed"))),
    });
  };

  return (
    <Dialog
      open={open}
      onClose={close}
      title={t("Rename “{{name}}”?", { name: project.name })}
      description={t("The identifier is derived from the name, and everything built on it moves along: URL and host names, containers, network, volumes, the SSH users, the project directory and the backups. The containers are recreated, so the project is briefly unavailable.")}
      footer={
        <>
          <Button onClick={close} disabled={rename.isPending}>
            {t("Cancel")}
          </Button>
          <Button variant="primary" onClick={submit} loading={rename.isPending} disabled={confirm !== project.slug || slug === "" || unchanged} icon={<Pencil className="size-4" />}>
            {t("Rename project")}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        {error && <Alert tone="red">{error}</Alert>}
        <Field label={t("Project name")} htmlFor="rename-name" hint={t("Identifier: {{slug}}", { slug: slug || "—" })}>
          <Input id="rename-name" value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" spellCheck={false} />
        </Field>
        <Field label={t("Directory")} htmlFor="rename-path" hint={follows ? t("Below the projects directory.") : t("This directory was chosen by hand and stays unless you change it.")}>
          <Input id="rename-path" value={dir} onChange={(e) => { setPath(e.target.value); setPathTouched(true); }} spellCheck={false} />
        </Field>
        {(hasDatabase || hasStorage) && (
          <Checkbox
            label={t("Keep the database and bucket names")}
            description={
              hasDatabase
                ? t("Otherwise the database and its login are renamed to {{name}} – its contents are moved, and anything with the old name written into it (a committed .env, an external client) has to be adjusted.", { name: slug.replace(/-/g, "_") || "—" })
                : t("Otherwise the bucket is renamed and its objects are moved into it.")
            }
            checked={keepDataNames}
            onChange={(e) => setKeepDataNames(e.target.checked)}
          />
        )}
        <Field label={t("Type {{slug}} to confirm", { slug: project.slug })} htmlFor="rename-confirm">
          <Input id="rename-confirm" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="off" spellCheck={false} />
        </Field>
        {rename.isPending && <CreateProgress slug={project.slug} action="rename" title={t("Renaming the project…")} hint={t("Volumes and the database contents move here; big projects take a moment.")} />}
      </div>
    </Dialog>
  );
}
