import { Check, Copy, Link2, Trash2, UserPlus, Users } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { useProjects } from "@/api/hooks";
import type { InviteResult, Role, UserAdmin } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, ErrorState, Field, Input, Select, Spinner, type Tone } from "@/components/ui";
import { copyText } from "@/lib/clipboard";
import { formatDateTime } from "@/lib/format";
import { errorText } from "@/lib/errors";
import { useAuth } from "@/features/auth/AuthContext";

/** The roles with what they allow, most powerful first. */
export function useRoleLabels() {
  const { t } = useTranslation();
  return {
    admin: t("Admin - everything, including users and settings"),
    developer: t("Developer - start, stop, terminal, actions, git, backups"),
    viewer: t("Viewer - look at projects, logs and status"),
    none: t("No access - only the projects given below"),
  } satisfies Record<Role, string>;
}

const roleShort = (t: (k: string) => string): Record<Role, string> => ({ admin: t("Admin"), developer: t("Developer"), viewer: t("Viewer"), none: t("No access") });

/** An invitation link with a copy button and its expiry. */
function InviteLink({ invite, onClose }: { invite: InviteResult; onClose: () => void }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  return (
    <Alert tone="green" title={t("Invitation link for {{name}}", { name: invite.user.username })}>
      <p className="mb-2 text-xs">{t("Pass this link on; it is shown only now and works once until {{when}}. Envoryx sends no mail.", { when: formatDateTime(invite.expiresAt) })}</p>
      <div className="flex items-start gap-2">
        <code className="min-w-0 flex-1 select-all break-all rounded-md bg-muted p-2 font-mono text-[11px] text-fg">{invite.inviteUrl}</code>
        <Button
          size="sm"
          icon={copied ? <Check className="size-3.5 text-emerald-500" /> : <Copy className="size-3.5" />}
          onClick={async () => {
            setCopied(await copyText(invite.inviteUrl));
            setTimeout(() => setCopied(false), 1500);
          }}
        >
          {t("Copy")}
        </Button>
        <Button size="sm" variant="ghost" onClick={onClose}>
          {t("Done")}
        </Button>
      </div>
    </Alert>
  );
}

/** Users, their roles and roles in particular projects. Admins only. */
export function UsersCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { user: me } = useAuth();
  const labels = useRoleLabels();
  const short = roleShort(t);
  const users = useQuery({ queryKey: ["users"], queryFn: async () => (await api.users.list()).users });
  const projects = useProjects();
  const [msg, setMsg] = useState<{ tone: Tone; text: string } | null>(null);
  const [invite, setInvite] = useState<InviteResult | null>(null);
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [role, setRole] = useState<Role>("developer");
  const [open, setOpen] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null);
  const refresh = () => void qc.invalidateQueries({ queryKey: ["users"] });
  const fail = (fallback: string) => (err: unknown) => setMsg({ tone: "red", text: errorText(err, t, fallback) });

  const create = useMutation({
    mutationFn: () => api.users.invite({ username: name.trim(), role }),
    onSuccess: (r) => {
      setInvite(r);
      setAdding(false);
      setName("");
      setMsg(null);
      refresh();
    },
    onError: fail(t("Inviting failed")),
  });
  const update = useMutation({
    mutationFn: ({ id, body }: { id: string; body: { role?: Role; disabled?: boolean } }) => api.users.update(id, body),
    onSuccess: refresh,
    onError: fail(t("Saving failed")),
  });
  const renew = useMutation({
    mutationFn: (id: string) => api.users.renewInvite(id),
    onSuccess: (r) => {
      setInvite(r);
      refresh();
    },
    onError: fail(t("Creating a new link failed")),
  });
  const remove = useMutation({
    mutationFn: (id: string) => api.users.remove(id),
    onSuccess: () => {
      setConfirmDelete(null);
      refresh();
    },
    onError: fail(t("Deleting failed")),
  });
  const setProjectRole = useMutation({
    mutationFn: ({ id, project, role }: { id: string; project: string; role: Role | "" }) => api.users.setProjectRole(id, project, role),
    onSuccess: refresh,
    onError: fail(t("Saving failed")),
  });

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <Users className="size-4 text-accent-500" aria-hidden /> {t("Users")}
          </span>
        }
        description={t("Everyone who may sign in, with a role for the whole instance and, where needed, another role in particular projects. A user without access to a project does not see it. API tokens never do more than their owner.")}
        actions={
          <Button variant="primary" icon={<UserPlus className="size-4" />} onClick={() => setAdding(true)}>
            {t("Invite user")}
          </Button>
        }
      />
      <div className="space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        {invite && <InviteLink invite={invite} onClose={() => setInvite(null)} />}
        {adding && (
          <div className="flex flex-wrap items-end gap-3 rounded-md border border-default p-3">
            <Field label={t("Username")} htmlFor="invite-name">
              <Input id="invite-name" value={name} autoFocus onChange={(e) => setName(e.target.value)} />
            </Field>
            <Field label={t("Role")} htmlFor="invite-role">
              <Select id="invite-role" value={role} onChange={(e) => setRole(e.target.value as Role)}>
                {(Object.keys(labels) as Role[]).map((r) => (
                  <option key={r} value={r}>
                    {labels[r]}
                  </option>
                ))}
              </Select>
            </Field>
            <Button variant="primary" loading={create.isPending} disabled={!name.trim()} onClick={() => create.mutate()}>
              {t("Create invitation")}
            </Button>
            <Button variant="ghost" onClick={() => setAdding(false)}>
              {t("Cancel")}
            </Button>
          </div>
        )}
        {users.isPending ? (
          <Spinner />
        ) : users.isError ? (
          <ErrorState message={errorText(users.error, t)} />
        ) : (
          <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
            {users.data.map((u: UserAdmin) => {
              const self = u.id === me?.id;
              const roles = Object.entries(u.projectRoles);
              return (
                <li key={u.id} className="space-y-3 px-3 py-2 text-sm">
                  <div className="flex flex-wrap items-center gap-3">
                    <span className="font-medium text-fg">{u.username}</span>
                    {self && <Badge>{t("you")}</Badge>}
                    {u.sso && <Badge tone="blue">SSO</Badge>}
                    {u.invited && <Badge tone="amber">{t("invited")}</Badge>}
                    {u.disabled && <Badge tone="red">{t("disabled")}</Badge>}
                    {roles.length > 0 && <span className="text-xs text-subtle">{t("{{count}} project roles", { count: roles.length })}</span>}
                    <span className="ml-auto flex flex-wrap items-center gap-2">
                      <div className="w-40">
                        <Select aria-label={t("Role of {{name}}", { name: u.username })} value={u.role} onChange={(e) => update.mutate({ id: u.id, body: { role: e.target.value as Role } })}>
                          {(Object.keys(short) as Role[]).map((r) => (
                            <option key={r} value={r}>
                              {short[r]}
                            </option>
                          ))}
                        </Select>
                      </div>
                      <Button size="sm" variant="ghost" onClick={() => setOpen(open === u.id ? null : u.id)}>
                        {t("Projects")}
                      </Button>
                      <Button size="sm" variant="ghost" icon={<Link2 className="size-3.5" />} loading={renew.isPending && renew.variables === u.id} onClick={() => renew.mutate(u.id)} title={t("A new link to set the password - also for a forgotten one")}>
                        {u.invited ? t("New invitation") : t("Reset password")}
                      </Button>
                      {!self && (
                        <Button size="sm" variant="ghost" onClick={() => update.mutate({ id: u.id, body: { disabled: !u.disabled } })}>
                          {u.disabled ? t("Enable") : t("Disable")}
                        </Button>
                      )}
                      {!self &&
                        (confirmDelete === u.id ? (
                          <Button size="sm" variant="danger" loading={remove.isPending} onClick={() => remove.mutate(u.id)}>
                            {t("Delete {{name}}", { name: u.username })}
                          </Button>
                        ) : (
                          <Button size="sm" variant="ghost" icon={<Trash2 className="size-3.5" />} aria-label={t("Delete {{name}}", { name: u.username })} onClick={() => setConfirmDelete(u.id)} />
                        ))}
                    </span>
                  </div>
                  {u.inviteExpiresAt && <p className="text-xs text-subtle">{t("The link works until {{when}}.", { when: formatDateTime(u.inviteExpiresAt) })}</p>}
                  {open === u.id && (
                    <div className="space-y-2 rounded-md bg-muted/40 p-3">
                      <p className="text-xs text-muted">{u.role === "admin" ? t("An admin may do everything in every project; project roles apply once the role changes.") : t("A role here replaces the user's role in that project; “from the role” leaves the global role.")}</p>
                      <ul className="grid gap-2 sm:grid-cols-2">
                        {(projects.data ?? []).filter((p) => !p.parentId).map((p) => (
                          <li key={p.id} className="flex items-center justify-between gap-2">
                            <span className="min-w-0 flex-1 truncate" title={p.name}>
                              {p.name}
                            </span>
                            <div className="w-40 shrink-0">
                              <Select
                                aria-label={t("Role of {{name}} in {{project}}", { name: u.username, project: p.name })}
                                value={u.projectRoles[p.id] ?? ""}
                                onChange={(e) => setProjectRole.mutate({ id: u.id, project: p.id, role: e.target.value as Role | "" })}
                              >
                                <option value="">{t("from the role")}</option>
                                {(["admin", "developer", "viewer", "none"] as Role[]).map((r) => (
                                  <option key={r} value={r}>
                                    {short[r]}
                                  </option>
                                ))}
                              </Select>
                            </div>
                          </li>
                        ))}
                      </ul>
                      <p className="text-xs text-subtle">{t("Branch environments get the roles of their parent when they are created.")}</p>
                    </div>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </Card>
  );
}
