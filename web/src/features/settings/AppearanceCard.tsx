import { Check } from "lucide-react";
import { useTranslation } from "react-i18next";
import { clsx } from "clsx";
import { Card, CardHeader } from "@/components/ui";
import { LogoMark } from "@/layout/Logo";
import { accents, useAccent, useLayoutWidth, type Accent, type LayoutWidth } from "@/layout/theme";

const accentLabel: Record<Accent, string> = {
  mint: "Mint", lime: "Lime", ocean: "Ocean", indigo: "Indigo", violet: "Violet",
  fuchsia: "Fuchsia", rose: "Rose", orange: "Orange", amber: "Amber", slate: "Graphite",
};
// Swatch colours: the 500 tone of each palette (see index.css).
const swatch: Record<Accent, string> = {
  mint: "#10c98f", lime: "#84cc16", ocean: "#0ea5e9", indigo: "#6366f1", violet: "#8b5cf6",
  fuchsia: "#d946ef", rose: "#f43f5e", orange: "#f97316", amber: "#f59e0b", slate: "#64748b",
};

const widths: { id: LayoutWidth; label: string; description: string }[] = [
  { id: "fluid", label: "Full width", description: "Pages use the whole window." },
  { id: "boxed", label: "Boxed", description: "Pages stay in a centred column, easier to read on wide screens." },
];

/** Per-browser accent colour and page width. The logo, buttons and highlights follow the accent; mint is the brand default. */
export function AppearanceCard() {
  const { t } = useTranslation();
  const [accent, setAccent] = useAccent();
  const [layout, setLayout] = useLayoutWidth();
  return (
    <Card>
      <CardHeader title={t("Appearance")} description={t("Accent colour and page width are stored in this browser; the logo, buttons and highlights follow the accent.")} />
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
      <div className="border-t border-default px-5 py-4">
        <p className="mb-2 text-sm font-medium text-fg" id="page-width">
          {t("Page width")}
        </p>
        <div role="radiogroup" aria-labelledby="page-width" className="grid gap-2 sm:grid-cols-2">
          {widths.map((w) => (
            <button
              key={w.id}
              type="button"
              role="radio"
              aria-checked={layout === w.id}
              onClick={() => setLayout(w.id)}
              className={clsx("flex items-start gap-3 rounded-md border p-3 text-left text-sm", layout === w.id ? "border-accent-500 bg-accent-500/10" : "border-default hover:bg-muted")}
            >
              <WidthIcon boxed={w.id === "boxed"} />
              <span>
                <span className="block font-medium text-fg">{t(w.label)}</span>
                <span className="block text-xs text-muted">{t(w.description)}</span>
              </span>
            </button>
          ))}
        </div>
      </div>
    </Card>
  );
}

/** A small window with the content either filling it or centred. */
function WidthIcon({ boxed }: { boxed: boolean }) {
  return (
    <svg viewBox="0 0 32 22" className="mt-0.5 h-5 w-7 shrink-0 text-muted" aria-hidden>
      <rect x="0.5" y="0.5" width="31" height="21" rx="2" fill="none" stroke="currentColor" />
      <rect x={boxed ? 9 : 3} y="4" width={boxed ? 14 : 26} height="14" rx="1" className="fill-accent-500/60" />
    </svg>
  );
}
