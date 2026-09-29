import { clsx } from "clsx";
import { Search } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { AppKind, ProjectTemplate } from "@/api/types";
import { Badge, Input } from "@/components/ui";

/** Short runtime names for the filter and the cards, in the order the filter shows them. */
export const runtimeNames: Record<AppKind, string> = { php: "PHP", node: "Node.js", python: "Python", go: "Go", ruby: "Ruby", java: "Java", dotnet: ".NET" };
const runtimeOrder: AppKind[] = ["php", "node", "python", "go", "ruby", "java", "dotnet"];

// What most people start with, shown first; the rest is one click (or a search) away.
const popular = ["laravel", "wordpress", "symfony", "next", "vite", "django", "rails", "spring-boot", "aspnet-webapi"];

/** Older backends omit the template runtime; every template was a PHP one then. */
export function templateRuntime(tpl: ProjectTemplate): AppKind {
  return tpl.runtime ?? "php";
}

/**
 * Every template of every runtime in one place, since people think "Laravel" or "Next.js"
 * before they think "PHP" or "Node.js". The filter and the search only narrow the list.
 */
export function TemplateGallery({ templates, selected, onSelect }: { templates: ProjectTemplate[]; selected: string; onSelect: (tpl: ProjectTemplate) => void }) {
  const { t } = useTranslation();
  const [filter, setFilter] = useState<AppKind | "">("");
  const [query, setQuery] = useState("");
  const [all, setAll] = useState(false);
  const kinds = runtimeOrder.filter((k) => templates.some((tpl) => templateRuntime(tpl) === k));
  const q = query.trim().toLowerCase();
  const matches = templates
    .filter((tpl) => (!filter || templateRuntime(tpl) === filter) && (!q || tpl.name.toLowerCase().includes(q) || tpl.description.toLowerCase().includes(q) || runtimeNames[templateRuntime(tpl)].toLowerCase().includes(q)))
    .sort((a, b) => rank(a) - rank(b));
  // Narrowed by a filter or search, everything that matches shows; otherwise the popular ones
  // (plus the chosen template, wherever it sits) until the user asks for all.
  const narrowed = !!filter || !!q;
  const shortList = matches.filter((tpl) => popular.includes(tpl.id) || tpl.id === selected);
  const shown = narrowed || all || shortList.length === 0 ? matches : shortList;

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-48 flex-1">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-subtle" aria-hidden />
          <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder={t("Search templates…")} aria-label={t("Search templates")} className="pl-8" />
        </div>
        <div className="flex flex-wrap gap-1" role="group" aria-label={t("Runtime")}>
          {(["", ...kinds] as const).map((k) => (
            <button
              key={k || "all"}
              type="button"
              aria-pressed={filter === k}
              onClick={() => setFilter(k)}
              className={clsx("rounded-full px-2.5 py-1 text-xs font-medium", filter === k ? "bg-accent-500/15 text-accent-700 dark:text-accent-300" : "text-muted hover:bg-muted hover:text-fg")}
            >
              {k ? runtimeNames[k] : t("All")}
            </button>
          ))}
        </div>
      </div>
      {shown.length === 0 ? (
        <p className="rounded-md border border-dashed border-default px-4 py-6 text-center text-sm text-muted">{t("No template matches. Start from an empty project and pick the runtime yourself.")}</p>
      ) : (
        <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
          {shown.map((tpl) => (
            <button
              key={tpl.id}
              type="button"
              aria-pressed={selected === tpl.id}
              onClick={() => onSelect(tpl)}
              className={clsx("flex flex-col items-start gap-1.5 rounded-lg border p-3 text-left text-sm transition-colors", selected === tpl.id ? "border-accent-500 bg-accent-500/5 ring-1 ring-accent-500" : "border-default hover:bg-muted")}
            >
              <span className="font-medium text-fg">{tpl.name}</span>
              <span className="flex flex-wrap gap-1">
                <Badge tone="blue">{runtimeNames[templateRuntime(tpl)]}</Badge>
                {(tpl.requiresDatabase || tpl.recommendedDatabase) && <Badge tone="amber">{t("Database")}</Badge>}
              </span>
              <span className="line-clamp-2 text-xs text-muted">{tpl.description}</span>
            </button>
          ))}
        </div>
      )}
      {!narrowed && shown.length < matches.length && (
        <button type="button" onClick={() => setAll(true)} className="text-sm font-medium text-accent-600 hover:underline dark:text-accent-300">
          {t("Show all {{count}} templates", { count: matches.length })}
        </button>
      )}
    </div>
  );
}

/** Popular templates first in their listed order, then by runtime (the backend sorts by name within). */
function rank(tpl: ProjectTemplate): number {
  const i = popular.indexOf(tpl.id);
  return i >= 0 ? i : 100 + runtimeOrder.indexOf(templateRuntime(tpl)) * 100;
}
