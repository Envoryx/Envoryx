import { Check } from "lucide-react";
import { useTranslation } from "react-i18next";
import { clsx } from "clsx";
import { Card, CardHeader } from "@/components/ui";
import { LogoMark } from "@/layout/Logo";
import { accents, useAccent, type Accent } from "@/layout/theme";

const accentLabel: Record<Accent, string> = {
  mint: "Mint", lime: "Lime", ocean: "Ocean", indigo: "Indigo", violet: "Violet",
  fuchsia: "Fuchsia", rose: "Rose", orange: "Orange", amber: "Amber", slate: "Graphite",
};
// Swatch colours: the 500 tone of each palette (see index.css).
const swatch: Record<Accent, string> = {
  mint: "#10c98f", lime: "#84cc16", ocean: "#0ea5e9", indigo: "#6366f1", violet: "#8b5cf6",
  fuchsia: "#d946ef", rose: "#f43f5e", orange: "#f97316", amber: "#f59e0b", slate: "#64748b",
};

/** Per-browser accent colour. The logo, buttons and highlights follow it; mint is the brand default. */
export function AppearanceCard() {
  const { t } = useTranslation();
  const [accent, setAccent] = useAccent();
  return (
    <Card>
      <CardHeader title={t("Appearance")} description={t("The accent colour is stored in this browser; the logo, buttons and highlights follow it.")} />
      <div className="flex flex-wrap items-center gap-6 px-5 py-4">
        <div role="radiogroup" aria-label={t("Accent colour")} className="flex flex-wrap gap-2">
          {accents.map((a) => (
            <button
              key={a}
              type="button"
              role="radio"
              aria-checked={accent === a}
              onClick={() => setAccent(a)}
              className={clsx(
                "inline-flex h-9 items-center gap-2 rounded-md border px-3 text-sm",
                accent === a ? "border-accent-500 bg-accent-500/10 text-fg" : "border-default text-muted hover:text-fg",
              )}
            >
              <span className="inline-flex size-4 items-center justify-center rounded-full" style={{ background: swatch[a] }} aria-hidden>
                {accent === a && <Check className="size-3 text-accent-ink" strokeWidth={3} />}
              </span>
              {t(accentLabel[a])}
            </button>
          ))}
        </div>
        <LogoMark size={28} className="ml-auto" />
      </div>
    </Card>
  );
}
