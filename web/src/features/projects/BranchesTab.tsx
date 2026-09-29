import { GitBranch, GitCommitHorizontal, Play, Plus, RefreshCw, Save } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { keys, useBranchEnvironments, useProject, useProjectLinks } from "@/api/hooks";
import type { BranchSettings, BranchState, Project, RemoteBranch } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Checkbox, Code, ErrorState, Field, Input, Select, Spinner, StatusDot, type Tone } from "@/components/ui";
import { containerStateTone, formatDateTime, formatRelative } from "@/lib/format";
import { errorText } from "@/lib/errors";

const textareaClass = "w-full rounded-md border border-default bg-elevated p-2 font-mono text-xs text-fg focus:border-accent-500 focus:outline-none";

const deployTone: Record<NonNullable<BranchState["deployStatus"]>, Tone> = { running: "blue", succeeded: "green", failed: "red" };

/** The deploy status as a badge; nothing before the first deploy. */
function DeployBadge({ state }: { state?: BranchState | undefined }) {
  const { t } = useTranslation();
  if (!state?.deployStatus) return null;
  const label = { running: t("deploying"), succeeded: t("deployed"), failed: t("deploy failed") }[state.deployStatus];
  return <Badge tone={deployTone[state.deployStatus]}>{label}</Badge>;
}

function shortCommit(commit?: string): string {
  return commit ? commit.slice(0, 8) : "";
}

/** Branch environments: on a parent its settings and environments, on an environment its deploy. */
export function BranchesTab({ project }: { project: Project }) {
  return project.parentId ? <EnvironmentCard project={project} /> : <ParentView project={project} />;
}

function ParentView({ project: p }: { project: Project }) {
  const { t } = useTranslation();
  const q = useBranchEnvironments(p.id);
  if (!p.git.url) {
    return <Alert tone="amber">{t("Branch environments are copies of this project on other branches of its repository. Set a repository under Git first.")}</Alert>;
  }
  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorState message={errorText(q.error, t)} />;
  return (
    <div className="space-y-6">
      <EnvironmentsCard project={p} environments={q.data.environments} lastPoll={q.data.lastPoll} watching={!!q.data.settings.watch} />
      <SettingsCard project={p} settings={q.data.settings} />
    </div>
  );
}

function EnvironmentsCard({ project: p, environments, lastPoll, watching }: { project: Project; environments: Project[]; lastPoll?: { at: string; error?: string } | undefined; watching: boolean }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const links = useProjectLinks();
  const [branches, setBranches] = useState<RemoteBranch[] | null>(null);
  const [branch, setBranch] = useState("");
  const [msg, setMsg] = useState<{ tone: Tone; text: string } | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["projects", p.id, "branches"] });
    void qc.invalidateQueries({ queryKey: keys.projects });
  };
  const load = useMutation({
    mutationFn: () => api.projects.remoteBranches(p.id),
    onSuccess: (r) => {
      setBranches(r.branches);
      setBranch(r.branches.find((b) => !b.environment && b.name !== p.git.branch)?.name ?? "");
    },
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Asking the repository failed")) }),
  });
  const create = useMutation({
    mutationFn: (name: string) => api.projects.createBranch(p.id, name),
    onSuccess: (r) => {
      setMsg({ tone: "green", text: t("{{name}} is ready.", { name: r.project.name }) });
      setBranches(null);
      refresh();
    },
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Creating the branch environment failed")) }),
  });
  const deploy = useMutation({
    mutationFn: (id: string) => api.projects.deploy(id),
    onSettled: refresh,
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Deploying failed")) }),
  });
  // The parent's own branch is the parent; it gets no environment.
  const free = (branches ?? []).filter((b) => !b.environment && b.name !== p.git.branch);

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <GitBranch className="size-4 text-accent-500" aria-hidden /> {t("Branch environments")}
          </span>
        }
        description={t("Copies of this project on other branches of its repository: files (with .env and the dependencies), database and bucket are copied, then the working tree switches to the branch and the deploy commands run. Each gets its own containers and URL.")}
      />
      <div className="space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        {watching && lastPoll && (
          <p className={`text-xs ${lastPoll.error ? "text-red-600 dark:text-red-300" : "text-muted"}`}>
            {lastPoll.error ? t("Last check of the repository {{when}} failed: {{error}}", { when: formatRelative(lastPoll.at, t), error: lastPoll.error }) : t("Repository last checked {{when}}.", { when: formatRelative(lastPoll.at, t) })}
          </p>
        )}
        {environments.length === 0 ? (
          <p className="text-sm text-muted">{t("No branch environments yet.")}</p>
        ) : (
          <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
            {environments.map((e) => {
              const st = e.branchState;
              const url = links(e).url;
              return (
                <li key={e.id} className="space-y-2 px-3 py-2 text-sm">
                  <div className="flex flex-wrap items-center gap-3">
                    <StatusDot tone={containerStateTone(e.status.state)} />
                    <Link to={`/projects/${e.id}`} className="font-medium text-fg hover:underline">
                      {e.git.branch}
                    </Link>
                    <span className="font-mono text-xs text-subtle">{e.slug}</span>
                    <DeployBadge state={st} />
                    {st?.commit && (
                      <span className="inline-flex items-center gap-1 font-mono text-xs text-subtle">
                        <GitCommitHorizontal className="size-3" aria-hidden /> {shortCommit(st.commit)}
                      </span>
                    )}
                    {st?.deployedAt && <span className="text-xs text-subtle" title={formatDateTime(st.deployedAt)}>{formatRelative(st.deployedAt, t)}</span>}
                    <span className="ml-auto flex items-center gap-2">
                      {url && (
                        <a href={url} target="_blank" rel="noopener noreferrer" className="font-mono text-xs text-accent-600 hover:underline dark:text-accent-300">
                          {url}
                        </a>
                      )}
                      {st?.deployOutput && (
                        <Button size="sm" variant="ghost" onClick={() => setOpen(open === e.id ? null : e.id)}>
                          {open === e.id ? t("Hide output") : t("Output")}
                        </Button>
                      )}
                      <Button size="sm" icon={<RefreshCw className="size-3.5" />} loading={deploy.isPending && deploy.variables === e.id} disabled={e.status.state !== "running" || st?.deployStatus === "running"} onClick={() => deploy.mutate(e.id)}>
                        {t("Deploy")}
                      </Button>
                    </span>
                  </div>
                  {open === e.id && st?.deployOutput && <pre className="max-h-72 overflow-auto rounded-md bg-muted p-2 font-mono text-[11px]">{st.deployOutput}</pre>}
                </li>
              );
            })}
          </ul>
        )}
        <div className="space-y-3 rounded-md border border-default p-3">
          <p className="text-sm font-medium text-fg">{t("New branch environment")}</p>
          {branches === null ? (
            <Button icon={<GitBranch className="size-4" />} loading={load.isPending} onClick={() => load.mutate()}>
              {t("Load the branches of the repository")}
            </Button>
          ) : free.length === 0 ? (
            <p className="text-sm text-muted">{t("Every branch of the repository already has an environment.")}</p>
          ) : (
            <div className="flex flex-wrap items-end gap-3">
              <Field label={t("Branch")} htmlFor="branch-env-branch">
                <Select id="branch-env-branch" value={branch} onChange={(e) => setBranch(e.target.value)}>
                  {free.map((b) => (
                    <option key={b.name} value={b.name}>
                      {b.name} ({shortCommit(b.commit)})
                    </option>
                  ))}
                </Select>
              </Field>
              <Button variant="primary" icon={<Plus className="size-4" />} loading={create.isPending} disabled={!branch} onClick={() => create.mutate(branch)}>
                {t("Create environment")}
              </Button>
            </div>
          )}
          <p className="text-xs text-subtle">{t("Copying takes as long as the project is big. The environment starts right away and is deployed once.")}</p>
        </div>
      </div>
    </Card>
  );
}

interface SettingsForm {
  watch: boolean;
  patterns: string;
  pollMinutes: string;
  idleStopDays: string;
  maxEnvironments: string;
  deploy: string;
}

function toForm(s: BranchSettings): SettingsForm {
  return {
    watch: !!s.watch,
    patterns: (s.patterns ?? []).join("\n"),
    pollMinutes: s.pollMinutes ? String(s.pollMinutes) : "",
    idleStopDays: s.idleStopDays ? String(s.idleStopDays) : "",
    maxEnvironments: s.maxEnvironments ? String(s.maxEnvironments) : "",
    deploy: (s.deploy ?? []).join("\n"),
  };
}

function fromForm(f: SettingsForm): BranchSettings {
  const lines = (s: string) => s.split("\n").map((l) => l.trim()).filter(Boolean);
  return { watch: f.watch, patterns: lines(f.patterns), pollMinutes: Number(f.pollMinutes) || 0, idleStopDays: Number(f.idleStopDays) || 0, maxEnvironments: Number(f.maxEnvironments) || 0, deploy: lines(f.deploy) };
}

function SettingsCard({ project: p, settings }: { project: Project; settings: BranchSettings }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [form, setForm] = useState<SettingsForm>(() => toForm(settings));
  const [msg, setMsg] = useState<{ tone: Tone; text: string } | null>(null);
  const stored = JSON.stringify(toForm(settings));
  useEffect(() => setForm(JSON.parse(stored) as SettingsForm), [stored]);
  const set = (patch: Partial<SettingsForm>) => setForm((f) => ({ ...f, ...patch }));
  const save = useMutation({
    mutationFn: () => api.projects.setBranchSettings(p.id, fromForm(form)),
    onSuccess: () => {
      setMsg({ tone: "green", text: t("Branch settings saved.") });
      void qc.invalidateQueries({ queryKey: ["projects", p.id, "branches"] });
    },
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
  });
  const dirty = JSON.stringify(form) !== stored;
  return (
    <Card>
      <CardHeader
        title={t("Branch settings")}
        description={t("How environments are deployed and, with watching on, kept in step with the repository.")}
        actions={
          <Button variant="primary" icon={<Save className="size-4" />} loading={save.isPending} disabled={!dirty} onClick={() => save.mutate()}>
            {t("Save")}
          </Button>
        }
      />
      <div className="space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        <Field label={t("Deploy commands")} htmlFor="branch-deploy" hint={t("One per line, run in the application container as the project owner after the environment was created or pulled; the first failing one stops the deploy. E.g. composer install, php artisan migrate --force, npm ci && npm run build.")}>
          <textarea id="branch-deploy" rows={4} className={textareaClass} value={form.deploy} onChange={(e) => set({ deploy: e.target.value })} placeholder={"composer install\nphp artisan migrate --force"} />
        </Field>
        <Checkbox
          label={t("Watch the repository")}
          description={t("Envoryx asks the repository regularly (git ls-remote, no webhook needed): a push to an environment's branch is pulled and deployed, an environment whose branch was deleted is deleted with its files and data, and a new branch that matches a pattern below gets an environment.")}
          checked={form.watch}
          onChange={(e) => set({ watch: e.target.checked })}
        />
        {form.watch && (
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("Branches with an automatic environment")} htmlFor="branch-patterns" hint={t("One pattern per line, e.g. feature/* - * does not match /. Empty creates none automatically.")}>
              <textarea id="branch-patterns" rows={3} className={textareaClass} value={form.patterns} onChange={(e) => set({ patterns: e.target.value })} placeholder={"feature/*\nfix/*"} />
            </Field>
            <div className="space-y-4">
              <Field label={t("Check every (minutes)")} htmlFor="branch-poll" hint={t("Empty: {{n}}", { n: 5 })}>
                <Input id="branch-poll" type="number" min={1} max={1440} value={form.pollMinutes} onChange={(e) => set({ pollMinutes: e.target.value })} />
              </Field>
              <Field label={t("At most automatic environments")} htmlFor="branch-max" hint={t("Empty: {{n}}", { n: 5 })}>
                <Input id="branch-max" type="number" min={1} max={50} value={form.maxEnvironments} onChange={(e) => set({ maxEnvironments: e.target.value })} />
              </Field>
            </div>
          </div>
        )}
        <div className="max-w-xs">
          <Field label={t("Stop idle environments after (days)")} htmlFor="branch-idle" hint={t("Counted from the last request through the proxy or start; empty never stops them. A stopped environment keeps its files and data.")}>
            <Input id="branch-idle" type="number" min={1} max={365} value={form.idleStopDays} onChange={(e) => set({ idleStopDays: e.target.value })} />
          </Field>
        </div>
      </div>
    </Card>
  );
}

function EnvironmentCard({ project: p }: { project: Project }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const parent = useProject(p.parentId!);
  const [msg, setMsg] = useState<{ tone: Tone; text: string } | null>(null);
  const st = p.branchState;
  const deploy = useMutation({
    mutationFn: (pull: boolean) => api.projects.deploy(p.id, pull),
    onSuccess: () => setMsg({ tone: "green", text: t("Deployed.") }),
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Deploying failed")) }),
    onSettled: () => void qc.invalidateQueries({ queryKey: keys.project(p.id) }),
  });
  const running = p.status.state === "running";
  const commands = parent.data?.branches?.deploy ?? [];
  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <GitBranch className="size-4 text-accent-500" aria-hidden /> {t("Branch environment")}
          </span>
        }
        description={t("A copy of its parent project on one branch. It is deleted with its files and data when the branch is deleted while the parent watches the repository.")}
        actions={
          <div className="flex gap-2">
            <Button size="sm" icon={<Play className="size-3.5" />} disabled={!running || deploy.isPending} onClick={() => deploy.mutate(false)}>
              {t("Run deploy commands")}
            </Button>
            <Button size="sm" variant="primary" icon={<RefreshCw className="size-3.5" />} loading={deploy.isPending} disabled={!running} onClick={() => deploy.mutate(true)}>
              {t("Pull and deploy")}
            </Button>
          </div>
        }
      />
      <div className="space-y-4 p-5 text-sm">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        {!running && <Alert tone="blue">{t("Start the environment to deploy it.")}</Alert>}
        <dl className="grid gap-x-6 gap-y-2 sm:grid-cols-[10rem_1fr]">
          <dt className="text-muted">{t("Parent project")}</dt>
          <dd>{parent.data ? <Link to={`/projects/${parent.data.id}`} className="text-accent-600 hover:underline dark:text-accent-300">{parent.data.name}</Link> : "…"}</dd>
          <dt className="text-muted">{t("Branch")}</dt>
          <dd className="font-mono text-xs">{p.git.branch}</dd>
          <dt className="text-muted">{t("Commit")}</dt>
          <dd className="font-mono text-xs">{shortCommit(st?.commit) || "-"}</dd>
          <dt className="text-muted">{t("Last deploy")}</dt>
          <dd className="flex items-center gap-2">
            <DeployBadge state={st} />
            {st?.deployedAt ? <span className="text-xs text-subtle">{formatDateTime(st.deployedAt)}</span> : !st?.deployStatus && <span className="text-xs text-subtle">{t("never")}</span>}
          </dd>
          <dt className="text-muted">{t("Deploy commands")}</dt>
          <dd>
            {commands.length === 0 ? (
              <span className="text-xs text-subtle">{t("None - set them in the Branches section of the parent project.")}</span>
            ) : (
              <ul className="space-y-1">
                {commands.map((c) => (
                  <li key={c}>
                    <Code>{c}</Code>
                  </li>
                ))}
              </ul>
            )}
          </dd>
        </dl>
        {st?.deployOutput && <pre className="max-h-96 overflow-auto rounded-md bg-muted p-2 font-mono text-[11px]">{st.deployOutput}</pre>}
      </div>
    </Card>
  );
}
