import { Save, TerminalSquare } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { Alert, Button, Card, CardHeader, Field, Spinner, type Tone } from "@/components/ui";
import { errorText } from "@/lib/errors";

/** The signed-in user's own public keys for the SSH server. */
export function MySshKeysCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["my-ssh-keys"], queryFn: async () => (await api.auth.sshKeys()).keys });
  const [keys, setKeys] = useState("");
  const [msg, setMsg] = useState<{ tone: Tone; text: string } | null>(null);
  useEffect(() => {
    if (q.data !== undefined) setKeys(q.data);
  }, [q.data]);
  const save = useMutation({
    mutationFn: () => api.auth.setSshKeys(keys),
    onSuccess: (r) => {
      qc.setQueryData(["my-ssh-keys"], r.keys);
      setMsg({ tone: "green", text: t("Your SSH keys are saved.") });
    },
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
  });
  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <TerminalSquare className="size-4 text-accent-500" aria-hidden /> {t("My SSH keys")}
          </span>
        }
        description={t("Sign in to the SSH server (IDEs, debuggers, SFTP) with your own key instead of an API token. A key acts with your roles: it opens the projects you may work in.")}
        actions={
          <Button variant="primary" icon={<Save className="size-4" />} loading={save.isPending} disabled={q.isPending || keys === q.data} onClick={() => save.mutate()}>
            {t("Save")}
          </Button>
        }
      />
      <div className="space-y-3 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        {q.isPending ? (
          <Spinner />
        ) : (
          <Field label={t("Public keys")} htmlFor="my-ssh-keys" hint={t("One key per line (authorized_keys format), e.g. the content of ~/.ssh/id_ed25519.pub. Lines starting with # are comments.")}>
            <textarea id="my-ssh-keys" rows={4} className="w-full rounded-md border border-default bg-elevated p-2 font-mono text-xs text-fg focus:border-accent-500 focus:outline-none" value={keys} onChange={(e) => setKeys(e.target.value)} placeholder="ssh-ed25519 AAAA… me@laptop" />
          </Field>
        )}
      </div>
    </Card>
  );
}
