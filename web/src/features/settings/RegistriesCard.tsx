import { Package, Plus, Save, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { RegistryLogin } from "@/api/types";
import { Alert, Button, Card, CardHeader, ErrorState, Field, Input, Spinner, type Tone } from "@/components/ui";
import { errorText } from "@/lib/errors";

/** Logins for private registries: custom runtime images are pulled with them, and Dockerfile builds use them for FROM. Admins only. */
export function RegistriesCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["registries"], queryFn: async () => (await api.registries.list()).registries });
  const [rows, setRows] = useState<RegistryLogin[] | null>(null);
  const [msg, setMsg] = useState<{ tone: Tone; text: string } | null>(null);
  useEffect(() => {
    if (q.data) setRows(q.data.map((r) => ({ ...r, password: "" })));
  }, [q.data]);
  const save = useMutation({
    mutationFn: (list: RegistryLogin[]) => api.registries.set(list.map(({ host, username, password }) => ({ host, username, password: password ?? "" }))),
    onSuccess: (r) => {
      qc.setQueryData(["registries"], r.registries);
      setMsg({ tone: "green", text: t("Registry logins saved.") });
    },
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
  });
  if (q.isPending || !rows) return q.isError ? <ErrorState message={errorText(q.error, t)} /> : <Spinner />;
  const set = (i: number, patch: Partial<RegistryLogin>) => setRows(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <Package className="size-4 text-accent-500" aria-hidden /> {t("Private registries")}
          </span>
        }
        description={t("Logins for registries that custom runtime images come from. Envoryx pulls with them and uses them for the FROM images of Dockerfile builds. Passwords and tokens are never shown again.")}
        actions={
          <Button variant="primary" icon={<Save className="size-4" />} loading={save.isPending} onClick={() => save.mutate(rows)}>
            {t("Save")}
          </Button>
        }
      />
      <div className="space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        {rows.length === 0 && <p className="text-sm text-muted">{t("No logins yet. Public images need none.")}</p>}
        {rows.map((r, i) => (
          <div key={i} className="grid items-end gap-3 sm:grid-cols-[1fr_1fr_1fr_auto]">
            <Field label={t("Registry")} htmlFor={`reg-host-${i}`} hint={i === 0 ? t("e.g. ghcr.io, registry.example.com:5000 or docker.io") : undefined}>
              <Input id={`reg-host-${i}`} value={r.host} onChange={(e) => set(i, { host: e.target.value })} />
            </Field>
            <Field label={t("Username")} htmlFor={`reg-user-${i}`}>
              <Input id={`reg-user-${i}`} autoComplete="off" value={r.username} onChange={(e) => set(i, { username: e.target.value })} />
            </Field>
            <Field label={t("Password or token")} htmlFor={`reg-pw-${i}`} hint={r.hasPassword ? t("Stored; leave empty to keep it.") : undefined}>
              <Input id={`reg-pw-${i}`} type="password" autoComplete="new-password" value={r.password ?? ""} onChange={(e) => set(i, { password: e.target.value })} />
            </Field>
            <Button variant="ghost" aria-label={t("Remove login")} icon={<Trash2 className="size-4" />} onClick={() => setRows(rows.filter((_, j) => j !== i))} />
          </div>
        ))}
        <Button icon={<Plus className="size-4" />} onClick={() => setRows([...rows, { host: "", username: "", password: "" }])}>
          {t("Add login")}
        </Button>
      </div>
    </Card>
  );
}
