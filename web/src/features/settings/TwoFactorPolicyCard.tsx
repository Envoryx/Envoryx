import { ShieldCheck } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { useSettingLocked, useSettings, useUpdateSettings } from "@/api/hooks";
import { LockedHint } from "@/features/plan/PlanCard";
import type { TwoFactorRequirement } from "@/api/types";
import { Alert, Button, Card, CardHeader, Dialog, Field, Select, Spinner, type Tone } from "@/components/ui";
import { errorText } from "@/lib/errors";
import { SupportRefusedNote } from "./SupportAccessCard";

/** Whom the instance requires a second factor of. Admins only; read-only to the support. */
export function TwoFactorPolicyCard({ readOnly = false }: { readOnly?: boolean }) {
  const { t } = useTranslation();
  const s = useSettings();
  const update = useUpdateSettings();
  // The admin's own factors: a policy that covers an admin without one sends them to the
  // setup page as soon as it is saved.
  const own = useQuery({ queryKey: ["two-factor"], queryFn: api.twoFactor.status });
  const [msg, setMsg] = useState<{ tone: Tone; text: string } | null>(null);
  const [confirm, setConfirm] = useState<TwoFactorRequirement | null>(null);
  const locked = useSettingLocked("twoFactorRequired");
  if (s.isPending) return <Spinner />;
  const current: TwoFactorRequirement = s.data?.twoFactorRequired ?? "off";
  const labels: Record<TwoFactorRequirement, string> = {
    off: t("Nobody - everyone decides for themselves"),
    admins: t("Admins"),
    all: t("Everyone"),
  };
  const noOwnFactor = !!own.data && own.data.available && !own.data.totp && own.data.passkeys.length === 0;
  const save = (value: TwoFactorRequirement) => {
    setConfirm(null);
    update.mutate(
      { twoFactorRequired: value },
      {
        onSuccess: () => setMsg({ tone: "green", text: t("Saved.") }),
        onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Saving failed")) }),
      },
    );
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
            disabled={readOnly || locked || update.isPending}
            onChange={(e) => {
              const value = e.target.value as TwoFactorRequirement;
              // Admins and everyone both cover the admin changing it.
              if (value !== "off" && noOwnFactor) setConfirm(value);
              else save(value);
            }}
          >
            {(Object.keys(labels) as TwoFactorRequirement[]).map((k) => (
              <option key={k} value={k}>
                {labels[k]}
              </option>
            ))}
          </Select>
          {locked && <LockedHint />}
        </Field>
        {current !== "off" && <p className="text-xs text-muted">{t("Lost a device? An admin resets the user's second factors in the list above; the rescue command envoryx admin reset-2fa does it from the container's shell.")}</p>}
      </div>
      <Dialog
        open={confirm !== null}
        onClose={() => setConfirm(null)}
        title={t("Require a second factor of yourself too?")}
        description={t("This covers you, and you have no second factor yet. Once it is saved, you are taken to set one up straight away (authenticator app or passkey), and nothing else works until you have. Have your phone or security key at hand.")}
        footer={
          <>
            <Button onClick={() => setConfirm(null)}>{t("Cancel")}</Button>
            <Button variant="primary" onClick={() => confirm && save(confirm)}>
              {t("Save and set up now")}
            </Button>
          </>
        }
      />
    </Card>
  );
}
