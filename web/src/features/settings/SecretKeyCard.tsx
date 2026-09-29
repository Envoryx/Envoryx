import { Eye, KeyRound, RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { Alert, Button, Card, CardHeader, Code, Dialog, ErrorState, Spinner, type Tone } from "@/components/ui";
import { errorText } from "@/lib/errors";
import { CopyRow } from "@/features/projects/DatabaseTab";

/** The key that encrypts the secrets at rest: where it comes from, showing it for safekeeping and replacing it. Admins only. */
export function SecretKeyCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["secretKey"], queryFn: async () => (await api.secretKey.get()).secretKey });
  const [key, setKey] = useState<string | null>(null);
  const [rotateOpen, setRotateOpen] = useState(false);
  const [msg, setMsg] = useState<{ tone: Tone; text: string } | null>(null);
  const reveal = useMutation({
    mutationFn: () => api.secretKey.reveal(),
    onSuccess: (r) => setKey(r.key),
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t) }),
  });
  const rotate = useMutation({
    mutationFn: () => api.secretKey.rotate(),
    onSuccess: (r) => {
      qc.setQueryData(["secretKey"], r.secretKey);
      setRotateOpen(false);
      setKey(null);
      setMsg({
        tone: "green",
        text: t("New key {{id}} in place: {{database}} database values, {{files}} files and {{backups}} project backups re-encrypted. Keep a copy of the new key.", { id: r.secretKey.keyId, ...r.resealed }),
      });
    },
    onError: (err) => {
      setRotateOpen(false);
      setMsg({ tone: "red", text: errorText(err, t) });
    },
  });
  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorState message={errorText(q.error, t)} />;
  const info = q.data;

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <KeyRound className="size-4 text-accent-500" aria-hidden /> {t("Secret key", { context: "instance" })}
          </span>
        }
        description={t("Passwords, tokens and keys that Envoryx stores (in the database, the notification, offsite and certificate settings and in project backups) are encrypted with this key. Instance backups never contain it: keep a copy, or an instance backup cannot be restored on another machine.")}
      />
      <div className="space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        <dl className="grid gap-x-6 gap-y-1.5 text-sm sm:grid-cols-[9rem_1fr]">
          <dt className="text-muted">{t("Key ID")}</dt>
          <dd className="font-mono text-xs">{info.keyId}</dd>
          <dt className="text-muted">{t("Source")}</dt>
          <dd className="text-xs">
            {info.source === "env" ? (
              <>
                {t("the environment variable")} <Code>{info.envKey}</Code>
              </>
            ) : (
              <>
                {t("the key file")} <Code>{info.path ?? ""}</Code>
              </>
            )}
          </dd>
        </dl>
        {key ? (
          <dl className="divide-y divide-[var(--border)]">
            <CopyRow label={t("Key")} value={key} secret />
          </dl>
        ) : (
          <Button size="sm" icon={<Eye className="size-4" />} loading={reveal.isPending} onClick={() => reveal.mutate()}>
            {t("Show key")}
          </Button>
        )}
        {info.source === "file" && (
          <p className="text-xs text-muted">
            {t("To keep the key out of /config (and out of appdata backups of it), set it as {{env}} in the container; Envoryx takes it from there and removes the key file at the next start.", { env: info.envKey })}
          </p>
        )}
        {info.canRotate ? (
          <Button size="sm" variant="danger" icon={<RefreshCw className="size-4" />} onClick={() => setRotateOpen(true)}>
            {t("Replace the key")}
          </Button>
        ) : (
          info.source === "env" && (
            <p className="text-xs text-muted">
              {t("To replace the key, set a new one as {{env}} and the current one as {{old}} for one start; Envoryx re-encrypts everything and the old variable can go.", { env: info.envKey, old: info.envOldKey })}
            </p>
          )
        )}
      </div>
      <Dialog
        open={rotateOpen}
        onClose={() => setRotateOpen(false)}
        title={t("Replace the secret key?")}
        footer={
          <>
            <Button onClick={() => setRotateOpen(false)}>{t("Cancel")}</Button>
            <Button variant="danger" loading={rotate.isPending} onClick={() => rotate.mutate()}>
              {t("Replace the key")}
            </Button>
          </>
        }
      >
        <p className="text-sm">
          {t("Envoryx creates a new key and re-encrypts every secret and every project backup with it. Instance backups made before keep needing the current key {{id}}: keep a copy of it as long as you may restore one of them.", { id: info.keyId })}
        </p>
      </Dialog>
    </Card>
  );
}
