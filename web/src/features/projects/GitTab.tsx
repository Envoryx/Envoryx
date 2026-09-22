import { Check, Copy, Download, GitBranch, GitCommitHorizontal, KeyRound, RefreshCw, Save } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useEffect, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { keys, useDeployKey, useGitStatus } from "@/api/hooks";
import type { GitResult, Project } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Code, Field, Input, Spinner } from "@/components/ui";
import { copyText } from "@/lib/clipboard";
import { formatDateTime } from "@/lib/format";
import { errorText, translateMessage } from "@/lib/errors";

function DeployKeyCard() {
  const { t } = useTranslation();
  const key = useDeployKey();
  const [copied, setCopied] = useState(false);
  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <KeyRound className="size-4 text-accent-500" aria-hidden /> {t("Deploy key")}
          </span>
        }
        description={t("Add this public key as a read-only deploy key to your repository (GitHub: Settings → Deploy keys) to clone via SSH.")}
      />
      <div className="p-5">
        {key.isPending ? (
          <Spinner />
        ) : key.isError ? (
          <Alert tone="red">{errorText(key.error, t)}</Alert>
        ) : (
          <div className="flex items-start gap-2">
            <code className="min-w-0 flex-1 select-all break-all rounded-md bg-muted p-3 font-mono text-[11px] text-fg">{key.data}</code>
            <Button
              size="sm"
              onClick={async () => {
                setCopied(await copyText(key.data));
                setTimeout(() => setCopied(false), 1500);
              }}
              icon={copied ? <Check className="size-3.5 text-emerald-500" /> : <Copy className="size-3.5" />}
            >
              {t("Copy")}
            </Button>
          </div>
        )}
      </div>
    </Card>
  );
}

export function GitTab({ project }: { project: Project }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const status = useGitStatus(project.id, true);
  const [url, setUrl] = useState(project.git.url);
  const [branch, setBranch] = useState(project.git.branch);
  const [username, setUsername] = useState(project.git.username);
  const [token, setToken] = useState("");
  const [clearToken, setClearToken] = useState(false);
  const [busy, setBusy] = useState<"save" | "clone" | "pull" | "checkout" | null>(null);
  const [result, setResult] = useState<GitResult | null>(null);
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const [checkoutRef, setCheckoutRef] = useState("");

  useEffect(() => {
    setUrl(project.git.url);
    setBranch(project.git.branch);
    setUsername(project.git.username);
  }, [project.git.url, project.git.branch, project.git.username]);

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["projects", project.id, "git"] });
    void qc.invalidateQueries({ queryKey: keys.project(project.id) });
  };
  const fail = (err: unknown, fallback: string) => setMsg({ tone: "red", text: errorText(err, t, fallback) });

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setMsg(null);
    setBusy("save");
    try {
      const body: { url: string; branch: string; username: string; token?: string } = { url: url.trim(), branch: branch.trim(), username: username.trim() };
      if (clearToken) body.token = "";
      else if (token) body.token = token;
      await api.git.set(project.id, body);
      setToken("");
      setClearToken(false);
      setMsg({ tone: "green", text: t("Repository settings saved.") });
      refresh();
    } catch (err) {
      fail(err, t("Saving failed"));
    } finally {
      setBusy(null);
    }
  };

  const run = async (kind: "clone" | "pull" | "checkout") => {
    setMsg(null);
    setResult(null);
    setBusy(kind);
    try {
      const res = kind === "clone" ? await api.git.clone(project.id) : kind === "pull" ? await api.git.pull(project.id) : await api.git.checkout(project.id, checkoutRef.trim());
      setResult(res.result);
      setMsg({ tone: "green", text: t("git {{kind}} finished.", { kind }) });
      refresh();
    } catch (err) {
      fail(err, t("git {{kind}} failed", { kind }));
      refresh();
    } finally {
      setBusy(null);
    }
  };

  const st = status.data;
  const isSSH = url.startsWith("git@") || url.startsWith("ssh://");

  return (
    <div className="space-y-6">
      {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
      <div className="grid gap-6 lg:grid-cols-2">
        <Card>
          <CardHeader
            title={
              <span className="flex items-center gap-2">
                <GitBranch className="size-4 text-accent-500" aria-hidden /> {t("Repository")}
              </span>
            }
            description={t("Clone and pull run as the project owner inside a short-lived container from the project's runtime image.")}
          />
          <form onSubmit={save} className="space-y-4 p-5">
            <Field label={t("Repository URL")} htmlFor="git-url" hint="https://…, git@host:path.git or ssh://…">
              <Input id="git-url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://github.com/you/project.git" spellCheck={false} />
            </Field>
            <Field label={t("Branch")} htmlFor="git-branch" hint={t("Used for clone; leave empty for the default branch.")}>
              <Input id="git-branch" value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="main" spellCheck={false} />
            </Field>
            {!isSSH && (
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label={t("Username (optional)")} htmlFor="git-user" hint={t("GitHub: leave empty. GitLab: oauth2.")}>
                  <Input id="git-user" value={username} onChange={(e) => setUsername(e.target.value)} placeholder="x-access-token" spellCheck={false} autoComplete="off" />
                </Field>
                <Field label={t("Access token")} htmlFor="git-token" hint={project.git.hasToken ? t("A token is stored. Enter a new one to replace it.") : t("For private HTTPS repositories.")}>
                  <Input id="git-token" type="password" value={token} onChange={(e) => { setToken(e.target.value); setClearToken(false); }} autoComplete="new-password" placeholder={project.git.hasToken ? "••••••••" : ""} />
                </Field>
              </div>
            )}
            {project.git.hasToken && !isSSH && (
              <label className="flex items-center gap-2 text-xs text-muted">
                <input type="checkbox" checked={clearToken} onChange={(e) => setClearToken(e.target.checked)} className="accent-accent-600" /> {t("Remove the stored token")}
              </label>
            )}
            {isSSH && <p className="text-xs text-muted">{t("SSH URLs authenticate with the Envoryx deploy key shown on the right.")}</p>}
            <div className="flex flex-wrap gap-2">
              <Button type="submit" variant="primary" loading={busy === "save"} icon={<Save className="size-4" />}>
                {t("Save")}
              </Button>
              {st && !st.isRepo && st.configured && (
                <Button onClick={() => void run("clone")} loading={busy === "clone"} disabled={busy !== null} icon={<Download className="size-4" />}>
                  {t("Clone now")}
                </Button>
              )}
            </div>
          </form>
        </Card>
        <DeployKeyCard />
      </div>

      <Card>
        <CardHeader
          title={
            <span className="flex items-center gap-2">
              <GitCommitHorizontal className="size-4 text-accent-500" aria-hidden /> {t("Working copy")}
            </span>
          }
          actions={
            st?.isRepo ? (
              <>
                <Button size="sm" onClick={() => void run("pull")} loading={busy === "pull"} disabled={busy !== null} icon={<RefreshCw className="size-3.5" />}>
                  {t("Pull")}
                </Button>
                <Button size="sm" variant="ghost" onClick={() => status.refetch()} aria-label={t("Refresh status")}>
                  <RefreshCw className="size-3.5" />
                </Button>
              </>
            ) : undefined
          }
        />
        <div className="p-5">
          {status.isPending ? (
            <Spinner />
          ) : status.isError ? (
            <Alert tone="red">{errorText(status.error, t)}</Alert>
          ) : !st?.isRepo ? (
            <p className="text-sm text-muted">{st?.configured ? t("The project directory is not a git repository yet. Clone the repository into the empty project directory.") : t("No repository configured.")}</p>
          ) : (
            <div className="space-y-4">
              {st.error && <Alert tone="red">{translateMessage(st.error, t)}</Alert>}
              <dl className="grid gap-x-8 gap-y-2 text-sm sm:grid-cols-[10rem_1fr]">
                <dt className="text-muted">{t("Branch")}</dt>
                <dd className="flex items-center gap-2">
                  <Code>{st.currentBranch || t("detached")}</Code>
                  {st.dirty > 0 ? <Badge tone="amber">{t("{{count}} uncommitted changes", { count: st.dirty })}</Badge> : <Badge tone="green">{t("clean")}</Badge>}
                </dd>
                <dt className="text-muted">{t("Last commit")}</dt>
                <dd>
                  <Code>{st.shortHash}</Code> {st.subject}
                  <span className="block text-xs text-subtle">
                    {st.author} · {st.date ? formatDateTime(st.date) : ""}
                  </span>
                </dd>
                <dt className="text-muted">{t("Remote")}</dt>
                <dd className="font-mono text-xs">{st.remote}</dd>
              </dl>
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  void run("checkout");
                }}
                className="flex items-end gap-2"
              >
                <Field label={t("Switch branch")} htmlFor="git-checkout" hint={t("Local or remote branch name; uncommitted changes are kept if they do not conflict.")}>
                  <Input id="git-checkout" value={checkoutRef} onChange={(e) => setCheckoutRef(e.target.value)} placeholder="develop" spellCheck={false} />
                </Field>
                <Button type="submit" loading={busy === "checkout"} disabled={!checkoutRef.trim() || busy !== null}>
                  {t("Checkout")}
                </Button>
              </form>
            </div>
          )}
          {result && (
            <pre className="mt-4 max-h-64 overflow-auto rounded-md bg-[#0f1115] p-3 font-mono text-[11px] leading-5 text-zinc-200">{result.output || t("(no output)")}</pre>
          )}
        </div>
      </Card>
    </div>
  );
}
