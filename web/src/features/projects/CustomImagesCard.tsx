import { Boxes, Hammer, Save } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { useCustomImage } from "@/api/hooks";
import type { Project, ProjectService } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Code, Field, Input, Select, type Tone } from "@/components/ui";
import { formatDateTime, serviceLabel } from "@/lib/format";
import { errorText } from "@/lib/errors";

/** The runtimes that can run an image of the user's; databases, services and web servers keep the vetted images. */
export const customImageKinds = ["php", "node", "python", "go", "ruby", "java", "dotnet"] as const;

type Source = "catalog" | "image" | "dockerfile";

function sourceOf(svc: ProjectService): Source {
  return svc.customImage?.image ? "image" : svc.customImage?.dockerfile ? "dockerfile" : "catalog";
}

function RuntimeImage({ project: p, svc }: { project: Project; svc: ProjectService }) {
  const { t } = useTranslation();
  const mutation = useCustomImage(p.id);
  const access = p.access ?? "admin";
  const c = svc.customImage;
  const [source, setSource] = useState<Source>(sourceOf(svc));
  const [image, setImage] = useState(c?.image ?? "");
  const [dockerfile, setDockerfile] = useState(c?.dockerfile ?? `.envoryx/${svc.kind}.Dockerfile`);
  const [msg, setMsg] = useState<{ tone: Tone; text: string } | null>(null);
  useEffect(() => {
    setSource(sourceOf(svc));
    setImage(svc.customImage?.image ?? "");
    setDockerfile(svc.customImage?.dockerfile ?? `.envoryx/${svc.kind}.Dockerfile`);
  }, [svc]);
  const stored = sourceOf(svc);
  const dirty = source !== stored || (source === "image" && image.trim() !== (c?.image ?? "")) || (source === "dockerfile" && dockerfile.trim() !== (c?.dockerfile ?? ""));
  const running = p.status.state === "running";
  const run = (vars: { image?: string; dockerfile?: string; rebuild?: boolean }, done: string) => {
    setMsg(null);
    mutation.mutate(
      { kind: svc.kind, ...vars },
      {
        onSuccess: () => setMsg({ tone: "green", text: done }),
        onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
      },
    );
  };
  const save = () =>
    run(
      source === "image" ? { image: image.trim() } : source === "dockerfile" ? { dockerfile: dockerfile.trim() } : {},
      running ? t("Saved and applied. Containers were restarted.") : t("Saved. Changes apply on next start."),
    );
  // Warnings belong to the image they were found in; a changed Dockerfile gets a new one.
  const warnings = c?.checkedImage === svc.image ? (c.warnings ?? []) : [];

  return (
    <div className="space-y-3 rounded-md border border-default p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <span className="font-medium">{serviceLabel(svc.kind)}</span>
          {stored === "catalog" ? <Badge tone="gray">{t("Envoryx image")}</Badge> : <Badge tone="blue">{stored === "image" ? t("registry image") : t("built from a Dockerfile")}</Badge>}
          {c?.buildFailed && <Badge tone="red">{t("build failed")}</Badge>}
        </div>
        <Code className="truncate text-xs">{svc.image}</Code>
      </div>
      {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
      {access === "admin" && (
        <div className="grid items-end gap-3 sm:grid-cols-[12rem_1fr_auto]">
          <Field label={t("Image")} htmlFor={`img-src-${svc.kind}`}>
            <Select id={`img-src-${svc.kind}`} value={source} onChange={(e) => setSource(e.target.value as Source)}>
              <option value="catalog">{t("Envoryx image")}</option>
              <option value="image">{t("Image from a registry")}</option>
              <option value="dockerfile">{t("Dockerfile in the project")}</option>
            </Select>
          </Field>
          {source === "image" ? (
            <Field label={t("Image reference")} htmlFor={`img-ref-${svc.kind}`}>
              <Input id={`img-ref-${svc.kind}`} value={image} placeholder={`ghcr.io/acme/${svc.kind}:latest`} onChange={(e) => setImage(e.target.value)} />
            </Field>
          ) : source === "dockerfile" ? (
            <Field label={t("Dockerfile")} htmlFor={`img-df-${svc.kind}`}>
              <Input id={`img-df-${svc.kind}`} value={dockerfile} onChange={(e) => setDockerfile(e.target.value)} />
            </Field>
          ) : (
            <div />
          )}
          <Button variant="primary" icon={<Save className="size-4" />} loading={mutation.isPending && !mutation.variables?.rebuild} disabled={!dirty || mutation.isPending} onClick={save}>
            {t("Save")}
          </Button>
        </div>
      )}
      {source === "dockerfile" && access === "admin" && (
        <p className="text-xs text-muted">
          {t("Relative to the project directory. Its directory is the build context, so keep it in a directory of its own. Envoryx builds it again whenever a file there changes. Start from an Envoryx image to keep every feature, for example:")}{" "}
          <Code>FROM {svc.image.startsWith("ghcr.io/envoryx/") ? svc.image : `ghcr.io/envoryx/envoryx-${svc.kind}:${svc.version}`}</Code>
        </p>
      )}
      {stored === "dockerfile" && access !== "read" && (
        <Button
          size="sm"
          icon={<Hammer className="size-4" />}
          loading={mutation.isPending && !!mutation.variables?.rebuild}
          disabled={mutation.isPending}
          onClick={() => run({ rebuild: true }, t("Image rebuilt."))}
        >
          {t("Rebuild without cache")}
        </Button>
      )}
      {warnings.length > 0 && (
        <Alert tone="amber" title={t("The image lacks what Envoryx expects")}>
          <ul className="list-disc space-y-0.5 pl-4">
            {warnings.map((w) => (
              <li key={w}>{w}</li>
            ))}
          </ul>
          <p className="mt-1 text-xs">{t("Envoryx uses the image anyway; the features named here do not work with it.")}</p>
        </Alert>
      )}
      {stored !== "catalog" && c?.checkedAt && warnings.length === 0 && c.checkedImage === svc.image && (
        <p className="text-xs text-muted">{t("Checked {{when}}: everything Envoryx needs is there.", { when: formatDateTime(c.checkedAt) })}</p>
      )}
      {c?.buildOutput && (
        <details open={!!c.buildFailed}>
          <summary className="cursor-pointer text-xs text-muted">{c.buildFailed ? t("Output of the failed build") : t("Output of the last build")}</summary>
          <pre className="mt-2 max-h-72 overflow-auto rounded-md bg-muted p-2 font-mono text-[11px] whitespace-pre-wrap">{c.buildOutput}</pre>
        </details>
      )}
    </div>
  );
}

/** Custom images of a project's runtimes: an image from a registry or a Dockerfile in the project. Workers and cron jobs follow their runtime. */
export function CustomImagesCard({ project: p }: { project: Project }) {
  const { t } = useTranslation();
  const services = p.services.filter((s) => s.enabled && (customImageKinds as readonly string[]).includes(s.kind));
  if (services.length === 0) return null;
  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <Boxes className="size-4 text-accent-500" aria-hidden /> {t("Runtime images")}
          </span>
        }
        description={t("Run a runtime in an image of your own: one from a registry, or a Dockerfile in the project that Envoryx builds. Workers and cron jobs use the same image. Envoryx checks the image and names what it lacks.")}
      />
      <div className="space-y-4 p-5">
        {services.map((s) => (
          <RuntimeImage key={s.kind} project={p} svc={s} />
        ))}
      </div>
    </Card>
  );
}
