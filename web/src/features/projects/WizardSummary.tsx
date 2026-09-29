import { ChevronRight } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Card, CardHeader } from "@/components/ui";

export interface SummaryRow {
  label: string;
  value: ReactNode;
  /** Where a click leads, so every choice can be changed from the summary. */
  target: string;
}

/** What the wizard will create, kept in view on every step. */
export function WizardSummary({ rows, onJump }: { rows: SummaryRow[]; onJump: (target: string) => void }) {
  const { t } = useTranslation();
  return (
    <Card className="lg:sticky lg:top-6">
      <CardHeader title={t("Summary")} />
      <ul className="divide-y divide-[var(--border)]">
        {rows.map((r) => (
          <li key={r.label}>
            <button type="button" onClick={() => onJump(r.target)} className="group flex w-full items-start gap-3 px-5 py-2.5 text-left text-sm hover:bg-muted">
              <span className="w-24 shrink-0 text-xs text-muted">{r.label}</span>
              <span className="min-w-0 flex-1 break-words text-fg">{r.value}</span>
              <ChevronRight className="mt-0.5 size-3.5 shrink-0 text-subtle opacity-0 group-hover:opacity-100" aria-hidden />
            </button>
          </li>
        ))}
      </ul>
    </Card>
  );
}
