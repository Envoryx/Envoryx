import { ShieldCheck } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { useSettings, useUpdateSettings } from "@/api/hooks";
import type { TwoFactorRequirement } from "@/api/types";
import { Alert, Card, CardHeader, Field, Select, Spinner, type Tone } from "@/components/ui";
import { errorText } from "@/lib/errors";
import { SupportRefusedNote } from "./SupportAccessCard";

/** Whom the instance requires a second factor of. Admins only; read-only to the support. */
export function TwoFactorPolicyCard({ readOnly = false }: { readOnly?: boolean }) {
  const { t } = useTranslation();
  const s = useSettings();
  const update = useUpdateSettings();
  const [msg, setMsg] = useState<{ tone: Tone; text: string } | null>(null);
  if (s.isPending) return <Spinner />;
  const current: TwoFactorRequirement = s.data?.twoFactorRequired ?? "off";
  const labels: Record<TwoFactorRequirement, string> = {
    off: t("Nobody - everyone decides for themselves"),
    admins: t("Admins"),
    all: t("Everyone"),
  };
  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <ShieldCheck className="size-4 text-accent-500" aria-hidden /> {t("Two-factor authentication")}
          </span>
        }
        description={t("Who has to set up a second factor (authenticator app or passkey). Whoever is covered and has none yet sets one up right after signing in, before anything else. Sign-ins through single sign-on are left to the identity provider.")}
      />
      <div className="space-y-3 p-5">
        {readOnly && <SupportRefusedNote />}
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        <Field label={t("Required of")} htmlFor="two-factor-required">
          <Select
            id="two-factor-required"
            value={current}
            disabled={readOnly || update.isPending}
            onChange={(e) =>
              update.mutate(
                { twoFactorRequired: e.target.value as TwoFactorRequirement },
                {
                  onSuccess: () => setMsg({ tone: "green", text: t("Saved.") }),
                  onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
                },
              )
            }
          >
            {(Object.keys(labels) as TwoFactorRequirement[]).map((k) => (
              <option key={k} value={k}>
                {labels[k]}
              </option>
            ))}
          </Select>
        </Field>
        {current !== "off" && <p className="text-xs text-muted">{t("Lost a device? An admin resets the user's second factors in the list above; the rescue command envoryx admin reset-2fa does it from the container's shell.")}</p>}
      </div>
    </Card>
  );
}
