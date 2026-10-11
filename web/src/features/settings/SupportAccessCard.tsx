import { LifeBuoy } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { SupportAccess } from "@/api/types";
import { Alert, Badge, Button, Card, CardHeader, Field, Select } from "@/components/ui";
import { errorText } from "@/lib/errors";
import { formatDateTime } from "@/lib/format";

const supportKey = ["support-access"] as const;

/** How long a new consent lasts, in hours; 0 until revoked. */
const durations = [
  { hours: 1, label: "1 hour" },
  { hours: 24, label: "24 hours" },
  { hours: 24 * 7, label: "7 days" },
  { hours: 0, label: "Until revoked" },
];

/**
 * The admins' consent to the hoster's support signing in, on an instance a hoster's
 * fleet manages. Nothing shows elsewhere.
 */
export function SupportAccessCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: supportKey, queryFn: api.supportAccess.get });
  const [hours, setHours] = useState(1);
  const [error, setError] = useState<string | null>(null);
  const save = useMutation({
    mutationFn: (body: SupportAccess) => api.supportAccess.set(body),
    onSuccess: (data) => {
      setError(null);
      qc.setQueryData(supportKey, data);
    },
    onError: (err) => setError(errorText(err, t)),
  });
  if (!q.data?.managed) return null;
  const access = q.data.access;
  const allow = () => save.mutate(hours ? { allowed: true, until: new Date(Date.now() + hours * 3600 * 1000).toISOString() } : { allowed: true });
  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <LifeBuoy className="size-4 text-accent-500" aria-hidden />
            {t("Support access")}
            {access.allowed ? <Badge tone="amber">{t("allowed")}</Badge> : <Badge>{t("off")}</Badge>}
          </span>
        }
        description={t("Lets your hoster's support sign in as an admin, e.g. to help with a problem. Every support login is in the audit log; taking the access back ends it at once.")}
      />
      <div className="space-y-4 p-5">
        {error && <Alert tone="red">{error}</Alert>}
        {access.allowed ? (
          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-sm text-fg">
              {access.until ? t("Your hoster's support may sign in until {{time}}.", { time: formatDateTime(access.until) }) : t("Your hoster's support may sign in until you take the access back.")}
            </p>
            <Button variant="danger" loading={save.isPending} onClick={() => save.mutate({ allowed: false })}>
              {t("Take back")}
            </Button>
          </div>
        ) : (
          <div className="flex flex-wrap items-end gap-3">
            <Field label={t("For")} htmlFor="support-duration">
              <Select id="support-duration" value={hours} onChange={(e) => setHours(Number(e.target.value))}>
                {durations.map((d) => (
                  <option key={d.hours} value={d.hours}>
                    {t(d.label)}
                  </option>
                ))}
              </Select>
            </Field>
            <Button variant="primary" loading={save.isPending} onClick={allow}>
              {t("Allow support access")}
            </Button>
          </div>
        )}
      </div>
    </Card>
  );
}
