import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { Bot, X } from "lucide-react";
import type { Activity } from "@/api/types";
import { formatDateTime } from "@/lib/format";

const seenKey = "envoryx.activity.seen";

function readSeen(): string {
  try {
    return localStorage.getItem(seenKey) ?? "";
  } catch {
    return "";
  }
}

/** Sentence for one autonomous action; the kinds are the backend's Activity kinds. */
export function activityText(a: Activity, t: TFunction): string {
  const items = a.items.join(", ");
  switch (a.kind) {
    case "projects.resumed":
      return t("Started the projects again that were running before the restart: {{items}}", { items });
    case "docker.orphans_removed":
      return t("Removed orphaned resources that belonged to no project: {{items}}", { items });
    default:
      return `${a.kind}: ${items}`;
  }
}

/**
 * What Envoryx did on its own since it started – projects resumed, orphans removed – so
 * nothing happens behind the user's back. Dismissing remembers the newest entry per browser;
 * the audit log keeps the durable record.
 */
export function ActivityNotice({ activity }: { activity: Activity[] | undefined }) {
  const { t } = useTranslation();
  const [seen, setSeen] = useState(readSeen);
  if (!activity || activity.length === 0) return null;
  const newest = activity[0]!.at;
  if (seen === newest) return null;
  const dismiss = () => {
    try {
      localStorage.setItem(seenKey, newest);
    } catch {
      /* private mode: the notice simply shows again next time */
    }
    setSeen(newest);
  };
  return (
    <div className="mb-6 rounded-lg px-4 py-3 text-sm ring-1 ring-inset ring-accent-500/20 bg-accent-500/10" role="status">
      <div className="flex items-start justify-between gap-3">
        <p className="flex items-center gap-2 font-medium">
          <Bot className="size-4 text-accent-500" aria-hidden />
          {t("Envoryx acted on its own")}
        </p>
        <button type="button" onClick={dismiss} className="rounded p-0.5 text-muted hover:text-fg" aria-label={t("Dismiss")}>
          <X className="size-4" aria-hidden />
        </button>
      </div>
      <ul className="mt-1 space-y-0.5 text-fg/80">
        {activity.map((a) => (
          <li key={a.at + a.kind}>
            <span className="text-subtle">{formatDateTime(a.at)}</span> · {activityText(a, t)}
          </li>
        ))}
      </ul>
      <p className="mt-1 text-xs text-subtle">{t("Every action is also recorded in the audit log (Settings → Audit log).")}</p>
    </div>
  );
}
