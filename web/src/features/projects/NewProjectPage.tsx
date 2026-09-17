import { clsx } from "clsx";
import { ArrowLeft, ArrowRight, Check, Rocket } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api, ApiError } from "@/api/client";
import { useCreateProject, usePublicHost, useRuntimes } from "@/api/hooks";
import type { CreateProjectRequest, EnvVar, PHPConfig, Preview } from "@/api/types";
import { Alert, Button, Card, Checkbox, Code, ErrorState, Field, Input, PageHeader, Select, Spinner } from "@/components/ui";
import { projectUrl } from "@/lib/format";
import { EnvEditor } from "./EnvEditor";
import { PhpConfigForm } from "./PhpConfigForm";

const steps = ["General", "Runtime", "Web server", "Database & services", "Environment", "Summary"] as const;

function slugify(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40);
}

interface Form {
  name: string;
  path: string;
  pathTouched: boolean;
  docroot: string;
  phpEnabled: boolean;
  phpVersion: string;
  phpConfig: PHPConfig;
  nodeEnabled: boolean;
  nodeVersion: string;
  webVersion: string;
  dbType: string; // "" = none
  dbVersion: string;
  dbExpose: boolean;
  redis: boolean;
  redisVersion: string;
  redisExpose: boolean;
  mailpit: boolean;
  gitUrl: string;
  gitBranch: string;
  gitUsername: string;
  gitToken: string;
  env: EnvVar[];
  createStarter: boolean;
  start: boolean;
}

export function NewProjectPage() {
  const runtimes = useRuntimes();
  const create = useCreateProject();
  const publicHost = usePublicHost();
  const navigate = useNavigate();
  const [step, setStep] = useState(0);
  const [form, setForm] = useState<Form | null>(null);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [submitError, setSubmitError] = useState<string | null>(null);

  useEffect(() => {
    if (runtimes.data && !form) {
      const php = runtimes.data.runtimes.find((r) => r.key === "php");
      const node = runtimes.data.runtimes.find((r) => r.key === "node");
      const caddy = runtimes.data.runtimes.find((r) => r.key === "caddy");
      setForm({
        name: "",
        path: "",
        pathTouched: false,
        docroot: "public",
        phpEnabled: true,
        phpVersion: php?.versions.find((v) => v.default)?.version ?? php?.versions[0]?.version ?? "",
        phpConfig: runtimes.data.phpDefaults,
        nodeEnabled: false,
        nodeVersion: node?.versions.find((v) => v.default)?.version ?? node?.versions[0]?.version ?? "",
        webVersion: caddy?.versions.find((v) => v.default)?.version ?? "",
        dbType: "",
        dbVersion: "",
        dbExpose: false,
        redis: false,
        redisVersion: runtimes.data.runtimes.find((r) => r.key === "redis")?.versions.find((v) => v.default)?.version ?? "",
        redisExpose: false,
        mailpit: false,
        gitUrl: "",
        gitBranch: "",
        gitUsername: "",
        gitToken: "",
        env: [],
        createStarter: true,
        start: true,
      });
    }
  }, [runtimes.data, form]);

  const request = useMemo<CreateProjectRequest | null>(() => {
    if (!form) return null;
    const req: CreateProjectRequest = {
      name: form.name.trim(),
      path: form.path.trim() || slugify(form.name),
      docroot: form.docroot.trim(),
      web: { type: "caddy", version: form.webVersion },
      env: form.env.filter((e) => e.key),
      createStarter: form.createStarter,
      start: form.start,
    };
    if (form.phpEnabled) req.php = { version: form.phpVersion, config: form.phpConfig };
    if (form.nodeEnabled) req.node = { version: form.nodeVersion };
    if (form.dbType) req.database = { type: form.dbType, version: form.dbVersion, exposePort: form.dbExpose };
    if (form.redis) req.redis = { version: form.redisVersion, exposePort: form.redisExpose };
    if (form.mailpit) req.mailpit = {};
    if (form.gitUrl.trim()) {
      const git: NonNullable<CreateProjectRequest["git"]> = { url: form.gitUrl.trim(), branch: form.gitBranch.trim(), username: form.gitUsername.trim() };
      if (form.gitToken) git.token = form.gitToken;
      req.git = git;
      req.createStarter = false;
    }
    return req;
  }, [form]);

  useEffect(() => {
    if (step !== steps.length - 1 || !request) return;
    let cancelled = false;
    setPreview(null);
    setPreviewError(null);
    api.projects
      .preview(request)
      .then((r) => {
        if (!cancelled) setPreview(r.preview);
      })
      .catch((err: unknown) => {
        if (!cancelled) setPreviewError(err instanceof ApiError ? err.message : "Preview failed");
      });
    return () => {
      cancelled = true;
    };
  }, [step, request]);

  if (runtimes.isPending || !form) return <Spinner label="Loading runtimes…" />;
  if (runtimes.isError) return <ErrorState message={runtimes.error.message} />;

  const rt = runtimes.data;
  const php = rt.runtimes.find((r) => r.key === "php");
  const node = rt.runtimes.find((r) => r.key === "node");
  const caddy = rt.runtimes.find((r) => r.key === "caddy");
  const databases = rt.runtimes.filter((r) => r.kind === "database");
  const services = rt.runtimes.filter((r) => r.kind === "service");
  const nameError = form.name.trim().length > 0 && form.name.trim().length < 2 ? "At least 2 characters." : slugify(form.name) === "" && form.name.trim() ? "Name must contain letters or digits." : undefined;
  const canContinue = step === 0 ? form.name.trim().length >= 2 && !nameError : true;
  const set = (patch: Partial<Form>) => setForm((f) => (f ? { ...f, ...patch } : f));

  const submit = () => {
    if (!request) return;
    setSubmitError(null);
    create.mutate(request, {
      onSuccess: (p) => navigate(`/projects/${p.id}`),
      onError: (err) => setSubmitError(err instanceof ApiError ? err.message : "Creating the project failed"),
    });
  };

  return (
    <div>
      <PageHeader title="New project" description="Staqio creates an isolated Docker environment for your project." />
      <div className="grid gap-6 lg:grid-cols-[14rem_1fr]">
        <ol className="flex gap-2 overflow-x-auto lg:flex-col lg:gap-1" aria-label="Steps">
          {steps.map((label, i) => (
            <li key={label}>
              <button
                type="button"
                onClick={() => i < step && setStep(i)}
                disabled={i > step}
                className={clsx(
                  "flex w-full items-center gap-2.5 whitespace-nowrap rounded-md px-2.5 py-2 text-left text-sm",
                  i === step ? "bg-accent-500/10 font-medium text-accent-600 dark:text-accent-300" : i < step ? "text-fg hover:bg-muted" : "text-subtle",
                )}
                aria-current={i === step ? "step" : undefined}
              >
                <span className={clsx("flex size-5 shrink-0 items-center justify-center rounded-full text-[11px] font-semibold", i < step ? "bg-accent-600 text-white" : i === step ? "bg-accent-500/20" : "bg-muted")}>
                  {i < step ? <Check className="size-3" /> : i + 1}
                </span>
                {label}
              </button>
            </li>
          ))}
        </ol>

        <Card className="p-6">
          {step === 0 && (
            <div className="space-y-5">
              <Field label="Project name" htmlFor="name" error={nameError} hint={form.name ? `Identifier: ${slugify(form.name) || "—"}` : "Displayed in the UI; the identifier is derived from it."}>
                <Input id="name" autoFocus value={form.name} onChange={(e) => set({ name: e.target.value, path: form.pathTouched ? form.path : "" })} placeholder="Shimly API" />
              </Field>
              <Field label="Project directory" htmlFor="path" hint="Relative to the projects folder (/projects). Created if it does not exist.">
                <div className="flex items-center gap-2">
                  <span className="text-sm text-subtle">/projects/</span>
                  <Input id="path" value={form.pathTouched ? form.path : slugify(form.name)} onChange={(e) => set({ path: e.target.value, pathTouched: true })} placeholder="shimly-api" spellCheck={false} />
                </div>
              </Field>
              <Field label="Document root" htmlFor="docroot" hint='Subfolder served by the web server, e.g. "public" for Laravel/Symfony. Leave empty for the project root.'>
                <Input id="docroot" value={form.docroot} onChange={(e) => set({ docroot: e.target.value })} placeholder="public" spellCheck={false} />
              </Field>
              <div className="space-y-4 rounded-md border border-default p-4">
                <p className="text-sm font-medium text-fg">Git repository (optional)</p>
                <Field label="Repository URL" htmlFor="git-url" hint="Cloned into the empty project directory. https://…, git@host:path.git or ssh://…">
                  <Input id="git-url" value={form.gitUrl} onChange={(e) => set({ gitUrl: e.target.value })} placeholder="https://github.com/you/project.git" spellCheck={false} />
                </Field>
                {form.gitUrl.trim() && (
                  <div className="grid gap-4 sm:grid-cols-3">
                    <Field label="Branch" htmlFor="git-branch" hint="Empty = default branch">
                      <Input id="git-branch" value={form.gitBranch} onChange={(e) => set({ gitBranch: e.target.value })} placeholder="main" spellCheck={false} />
                    </Field>
                    {!(form.gitUrl.startsWith("git@") || form.gitUrl.startsWith("ssh://")) ? (
                      <>
                        <Field label="Username (optional)" htmlFor="git-user">
                          <Input id="git-user" value={form.gitUsername} onChange={(e) => set({ gitUsername: e.target.value })} placeholder="x-access-token" autoComplete="off" />
                        </Field>
                        <Field label="Access token" htmlFor="git-token" hint="Only for private repositories">
                          <Input id="git-token" type="password" value={form.gitToken} onChange={(e) => set({ gitToken: e.target.value })} autoComplete="new-password" />
                        </Field>
                      </>
                    ) : (
                      <p className="self-end pb-2 text-xs text-muted sm:col-span-2">SSH uses the Staqio deploy key (Settings → Deploy key); add it to the repository first.</p>
                    )}
                  </div>
                )}
              </div>
            </div>
          )}

          {step === 1 && php && (
            <div className="space-y-6">
              <Checkbox label="Enable PHP" description="Runs PHP-FPM in its own container. Disable for static sites." checked={form.phpEnabled} onChange={(e) => set({ phpEnabled: e.target.checked })} />
              {form.phpEnabled && (
                <>
                  <Field label="PHP version" htmlFor="php-version">
                    <Select id="php-version" value={form.phpVersion} onChange={(e) => set({ phpVersion: e.target.value })}>
                      {php.versions.map((v) => (
                        <option key={v.version} value={v.version}>
                          {v.label}
                          {v.eol ? " (end of life)" : v.preview ? " (preview)" : ""}
                        </option>
                      ))}
                    </Select>
                  </Field>
                  <PhpConfigForm value={form.phpConfig} onChange={(c) => set({ phpConfig: c })} extensions={rt.phpExtensions} />
                </>
              )}
              {node && (
                <div className="space-y-4 rounded-md border border-default p-4">
                  <Checkbox label="Enable Node.js" description="Toolchain container with npm, pnpm and yarn for asset builds. Runs idle; commands run via Actions or the terminal." checked={form.nodeEnabled} onChange={(e) => set({ nodeEnabled: e.target.checked })} />
                  {form.nodeEnabled && (
                    <Field label="Node.js version" htmlFor="node-version">
                      <Select id="node-version" value={form.nodeVersion} onChange={(e) => set({ nodeVersion: e.target.value })}>
                        {node.versions.map((v) => (
                          <option key={v.version} value={v.version}>
                            {v.label}
                            {v.eol ? " (end of life)" : v.preview ? " (preview)" : ""}
                          </option>
                        ))}
                      </Select>
                    </Field>
                  )}
                </div>
              )}
              <p className="text-xs text-subtle">Composer ships with the PHP image.</p>
            </div>
          )}

          {step === 2 && caddy && (
            <div className="space-y-5">
              <Field label="Web server" htmlFor="web">
                <Select id="web" value="caddy" disabled>
                  <option value="caddy">Caddy</option>
                </Select>
              </Field>
              <Field label="Version" htmlFor="web-version">
                <Select id="web-version" value={form.webVersion} onChange={(e) => set({ webVersion: e.target.value })}>
                  {caddy.versions.map((v) => (
                    <option key={v.version} value={v.version}>
                      {v.label}
                    </option>
                  ))}
                </Select>
              </Field>
              <p className="text-sm text-muted">
                Caddy serves static files from the document root and forwards PHP requests to the PHP container via FastCGI. The project is published on an automatically assigned port; domain routing follows in Phase 4.
              </p>
            </div>
          )}

          {step === 3 && (
            <div className="space-y-6">
              <div>
                <p className="mb-2 text-sm font-medium text-fg">Database</p>
                <div className="grid gap-2 sm:grid-cols-2" role="radiogroup" aria-label="Database">
                  <label className={clsx("flex items-center gap-2.5 rounded-md border px-3 py-2 text-sm cursor-pointer", form.dbType === "" ? "border-accent-500 bg-accent-500/5" : "border-default hover:bg-muted")}>
                    <input type="radio" name="db" className="accent-accent-600" checked={form.dbType === ""} onChange={() => set({ dbType: "", dbVersion: "" })} /> None
                  </label>
                  {databases.map((d) => (
                    <label
                      key={d.key}
                      className={clsx("flex items-center gap-2.5 rounded-md border px-3 py-2 text-sm", !d.available ? "border-default opacity-60" : form.dbType === d.key ? "border-accent-500 bg-accent-500/5 cursor-pointer" : "border-default hover:bg-muted cursor-pointer")}
                      title={d.description}
                    >
                      <input
                        type="radio"
                        name="db"
                        className="accent-accent-600"
                        disabled={!d.available}
                        checked={form.dbType === d.key}
                        onChange={() => set({ dbType: d.key, dbVersion: d.versions.find((v) => v.default)?.version ?? d.versions[0]?.version ?? "" })}
                      />
                      {d.name}
                      {!d.available && <span className="text-xs text-subtle">soon</span>}
                    </label>
                  ))}
                </div>
              </div>
              {form.dbType && (
                <div className="space-y-4 rounded-md border border-default p-4">
                  <Field label="Version" htmlFor="db-version" hint="Upgrades between versions run on the same data volume; downgrades are not possible.">
                    <Select id="db-version" value={form.dbVersion} onChange={(e) => set({ dbVersion: e.target.value })}>
                      {databases
                        .find((d) => d.key === form.dbType)
                        ?.versions.map((v) => (
                          <option key={v.version} value={v.version}>
                            {v.label}
                          </option>
                        ))}
                    </Select>
                  </Field>
                  <Checkbox
                    label="Publish database port on the host"
                    description="Lets you connect from your workstation with TablePlus, DBeaver, etc. The port is assigned automatically."
                    checked={form.dbExpose}
                    onChange={(e) => set({ dbExpose: e.target.checked })}
                  />
                  <p className="text-sm text-muted">
                    Staqio generates secure credentials and injects <Code>DB_HOST</Code>, <Code>DB_DATABASE</Code>, <Code>DB_USERNAME</Code>, <Code>DB_PASSWORD</Code> and <Code>DATABASE_URL</Code> into the PHP container. Data lives in a persistent Docker volume.
                  </p>
                </div>
              )}
              <div className="space-y-3">
                <p className="text-sm font-medium text-fg">Additional services</p>
                <div className="rounded-md border border-default p-4 space-y-3">
                  <Checkbox label="Redis" description="Cache and queue backend with a persistent volume. Injects REDIS_HOST, REDIS_PORT and REDIS_URL." checked={form.redis} onChange={(e) => set({ redis: e.target.checked })} />
                  {form.redis && (
                    <div className="grid gap-4 pl-7 sm:grid-cols-2">
                      <Field label="Redis version" htmlFor="redis-version">
                        <Select id="redis-version" value={form.redisVersion} onChange={(e) => set({ redisVersion: e.target.value })}>
                          {services
                            .find((s) => s.key === "redis")
                            ?.versions.map((v) => (
                              <option key={v.version} value={v.version}>
                                {v.label}
                              </option>
                            ))}
                        </Select>
                      </Field>
                      <div className="self-end pb-1">
                        <Checkbox label="Publish port on the host" description="For RedisInsight etc." checked={form.redisExpose} onChange={(e) => set({ redisExpose: e.target.checked })} />
                      </div>
                    </div>
                  )}
                </div>
                <div className="rounded-md border border-default p-4">
                  <Checkbox label="Mailpit" description="Catches all outgoing mail and shows it in a web inbox (published on its own port). Injects MAIL_* and MAILER_DSN." checked={form.mailpit} onChange={(e) => set({ mailpit: e.target.checked })} />
                </div>
              </div>
            </div>
          )}

          {step === 4 && (
            <div className="space-y-4">
              <p className="text-sm text-muted">Variables are available to all containers of this project (e.g. PHP via <Code>getenv()</Code>). Mark secrets to mask them in the UI.</p>
              <EnvEditor value={form.env} onChange={(env) => set({ env })} />
            </div>
          )}

          {step === 5 && (
            <div className="space-y-5">
              {previewError ? (
                <Alert tone="red" title="Cannot create this project">
                  {previewError}
                </Alert>
              ) : !preview ? (
                <Spinner label="Calculating plan…" />
              ) : (
                <>
                  {preview.warnings.length > 0 && (
                    <Alert tone="amber">
                      <ul className="list-disc pl-4">
                        {preview.warnings.map((w, i) => (
                          <li key={i}>{w}</li>
                        ))}
                      </ul>
                    </Alert>
                  )}
                  <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-[10rem_1fr]">
                    <dt className="text-muted">Identifier</dt>
                    <dd className="font-mono text-xs">{preview.slug}</dd>
                    <dt className="text-muted">Files</dt>
                    <dd className="font-mono text-xs">
                      {preview.path} <span className="text-subtle">(host: {preview.hostPath})</span>
                    </dd>
                    <dt className="text-muted">URL</dt>
                    <dd className="font-mono text-xs">{projectUrl(preview.httpPort, publicHost)}</dd>
                    <dt className="text-muted">Network</dt>
                    <dd className="font-mono text-xs">{preview.network}</dd>
                  </dl>
                  <div>
                    <p className="mb-2 text-sm font-medium text-fg">Containers</p>
                    <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
                      {preview.containers.map((c) => (
                        <li key={c.name} className="px-3 py-2 text-xs">
                          <div className="flex items-center justify-between gap-3">
                            <span className="font-mono font-medium text-fg">{c.name}</span>
                            <span className="font-mono text-subtle">{c.image}</span>
                          </div>
                          <ul className="mt-1 space-y-0.5 font-mono text-[11px] text-muted">
                            {c.ports.map((p) => (
                              <li key={p}>port {p}</li>
                            ))}
                            {c.mounts.map((m) => (
                              <li key={m} className="truncate">
                                mount {m}
                              </li>
                            ))}
                          </ul>
                        </li>
                      ))}
                    </ul>
                  </div>
                  {preview.volumes.length > 0 && (
                    <p className="text-sm text-muted">
                      Volumes: <Code>{preview.volumes.join(", ")}</Code>
                    </p>
                  )}
                  <div className="space-y-3 border-t border-default pt-4">
                    {form.gitUrl.trim() ? (
                      <p className="text-sm text-muted">
                        Repository <Code>{form.gitUrl.trim()}</Code> will be cloned into the project directory.
                      </p>
                    ) : (
                      <Checkbox label="Create starter index.php" description="Only if the document root is empty." checked={form.createStarter} onChange={(e) => set({ createStarter: e.target.checked })} />
                    )}
                    <Checkbox label="Start project after creation" checked={form.start} onChange={(e) => set({ start: e.target.checked })} />
                  </div>
                  {submitError && (
                    <Alert tone="red" title="Creation failed">
                      {submitError}
                    </Alert>
                  )}
                </>
              )}
            </div>
          )}

          <div className="mt-8 flex items-center justify-between border-t border-default pt-4">
            <Button variant="ghost" onClick={() => (step === 0 ? navigate("/projects") : setStep(step - 1))} icon={<ArrowLeft className="size-4" />} disabled={create.isPending}>
              {step === 0 ? "Cancel" : "Back"}
            </Button>
            {step < steps.length - 1 ? (
              <Button variant="primary" onClick={() => setStep(step + 1)} disabled={!canContinue} icon={<ArrowRight className="size-4" />}>
                Continue
              </Button>
            ) : (
              <Button variant="primary" onClick={submit} loading={create.isPending} disabled={!preview || !!previewError} icon={<Rocket className="size-4" />}>
                Create project
              </Button>
            )}
          </div>
        </Card>
      </div>
    </div>
  );
}
