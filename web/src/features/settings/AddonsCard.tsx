import { Download, Package, Pencil, Plus, Trash2, Upload } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { usePlanAllows } from "@/api/hooks";
import { NotInPlan } from "@/features/plan/PlanCard";
import type { ExampleAddon, InstalledAddon } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Dialog, ErrorState, Field, Input, Spinner, type Tone } from "@/components/ui";
import { errorText } from "@/lib/errors";

/** A starting point for a new addon file. */
const template = `name: my-addon
title: My addon
description: What it is for.
versions:
  - version: "1"
    image: example/image:1
    default: true
port: 8080
webUI: true
env:
  PASSWORD: "{{secret.password}}"
secrets: [password]
volumes:
  - name: data
    path: /data
inject:
  MY_ADDON_URL: "http://{{host}}:{{port}}"
`;

/** Installed addon files, the examples Envoryx ships, and installing from a file or URL. Admins only. */
export function AddonsCard() {
  const { t } = useTranslation();
  const allowed = usePlanAllows("addons");
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["addons"], queryFn: () => api.addons.list() });
  const [msg, setMsg] = useState<{ tone: Tone; text: string } | null>(null);
  const [editor, setEditor] = useState<{ title: string; source: string } | null>(null);
  const [url, setUrl] = useState("");
  const done = (text: string) => {
    void qc.invalidateQueries({ queryKey: ["addons"] });
    setMsg({ tone: "green", text });
  };
  const fail = (err: unknown) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) });
  const install = useMutation({
    mutationFn: (body: { source?: string; url?: string }) => api.addons.install(body),
    onSuccess: (r) => {
      setEditor(null);
      setUrl("");
      done(t("Addon {{name}} installed.", { name: r.addon.title }));
    },
    onError: fail,
  });
  const remove = useMutation({
    mutationFn: (name: string) => api.addons.remove(name),
    onSuccess: (_r, name) => done(t("Addon {{name}} removed.", { name })),
    onError: fail,
  });
  const edit = async (a: InstalledAddon) => {
    try {
      const r = await api.addons.get(a.name);
      setEditor({ title: t("Edit {{name}}", { name: a.title || a.name }), source: r.source });
    } catch (err) {
      fail(err);
    }
  };
  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorState message={errorText(q.error, t)} />;
  const installed = new Set(q.data.addons.map((a) => a.name));
  if (!allowed) {
    return (
      <Card>
        <CardHeader title={t("Addons")} />
        <div className="p-5">
          <NotInPlan feature="addons" />
        </div>
      </Card>
    );
  }

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader
          title={
            <span className="flex items-center gap-2">
              <Package className="size-4 text-accent-500" aria-hidden /> {t("Addons")}
            </span>
          }
          description={t("Addons add services described in a YAML file: one container per project with its image, variables, volumes and an optional web UI. Addons get no host directories, host network or privileges. Projects add installed addons in their Services section.")}
          actions={
            <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setEditor({ title: t("New addon"), source: template })}>
              {t("New addon")}
            </Button>
          }
        />
        <div className="space-y-4 p-5">
          {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
          {q.data.addons.length === 0 && <p className="text-sm text-muted">{t("No addons installed yet. Start from an example below, write a file or install one from a URL.")}</p>}
          <ul className="divide-y divide-[var(--border)]">
            {q.data.addons.map((a) => (
              <li key={a.file} className="flex flex-wrap items-center justify-between gap-3 py-2">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="font-medium">{a.title || a.name}</span>
                    <span className="font-mono text-xs text-subtle">{a.name}</span>
                    {a.error ? <Badge tone="red">{t("invalid")}</Badge> : <Badge tone="gray">{a.versions.map((v) => v.version).join(", ")}</Badge>}
                  </div>
                  {a.error ? <p className="text-xs text-red-600 dark:text-red-400">{a.error}</p> : a.description && <p className="text-xs text-muted">{a.description}</p>}
                  {a.projects.length > 0 && <p className="text-xs text-subtle">{t("Used by {{projects}}", { projects: a.projects.join(", ") })}</p>}
                </div>
                <div className="flex gap-2">
                  <Button size="sm" icon={<Pencil className="size-4" />} onClick={() => void edit(a)}>
                    {t("Edit")}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    aria-label={t("Remove {{name}}", { name: a.name })}
                    icon={<Trash2 className="size-4" />}
                    disabled={a.projects.length > 0}
                    title={a.projects.length > 0 ? t("Remove it from the projects first") : undefined}
                    loading={remove.isPending && remove.variables === a.name}
                    onClick={() => remove.mutate(a.name)}
                  />
                </div>
              </li>
            ))}
          </ul>
          <div className="flex flex-wrap items-end gap-3">
            <Field label={t("Install from a URL")} htmlFor="addon-url" hint={t("An addon file over http(s), e.g. a raw GitHub link.")}>
              <Input id="addon-url" className="min-w-80" value={url} placeholder="https://raw.githubusercontent.com/…/addon.yml" onChange={(e) => setUrl(e.target.value)} />
            </Field>
            <Button icon={<Download className="size-4" />} disabled={!url.trim()} loading={install.isPending && !!install.variables?.url} onClick={() => install.mutate({ url: url.trim() })}>
              {t("Install")}
            </Button>
          </div>
        </div>
      </Card>

      <Card>
        <CardHeader title={t("Examples")} description={t("Addon files that ship with Envoryx. Install one as it is or adjust it first.")} />
        <ul className="divide-y divide-[var(--border)] px-5">
          {q.data.examples.map((ex: ExampleAddon) => (
            <li key={ex.name} className="flex flex-wrap items-center justify-between gap-3 py-2">
              <div className="min-w-0">
                <span className="font-medium">{ex.title}</span> <span className="font-mono text-xs text-subtle">{ex.name}</span>
                {ex.description && <p className="text-xs text-muted">{ex.description}</p>}
              </div>
              <div className="flex gap-2">
                <Button size="sm" icon={<Pencil className="size-4" />} onClick={() => setEditor({ title: t("New addon"), source: ex.source })}>
                  {t("Adjust")}
                </Button>
                <Button size="sm" icon={<Upload className="size-4" />} loading={install.isPending && install.variables?.source === ex.source} onClick={() => install.mutate({ source: ex.source })}>
                  {installed.has(ex.name) ? t("Reinstall") : t("Install")}
                </Button>
              </div>
            </li>
          ))}
        </ul>
      </Card>

      <Dialog
        open={!!editor}
        onClose={() => setEditor(null)}
        title={editor?.title ?? ""}
        description={t("The file is checked when you save; a new version of an installed addon reaches the projects using it at their next start.")}
        footer={
          <>
            <Button onClick={() => setEditor(null)}>{t("Cancel")}</Button>
            <Button variant="primary" loading={install.isPending} onClick={() => editor && install.mutate({ source: editor.source })}>
              {t("Save")}
            </Button>
          </>
        }
      >
        {install.isError && <Alert tone="red">{errorText(install.error, t)}</Alert>}
        <textarea
          aria-label={t("Addon file")}
          className="mt-2 h-96 w-full rounded-md border border-default bg-elevated p-2 font-mono text-xs"
          spellCheck={false}
          value={editor?.source ?? ""}
          onChange={(e) => editor && setEditor({ ...editor, source: e.target.value })}
        />
      </Dialog>
    </div>
  );
}
