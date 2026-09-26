import { Package, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { Alert, Button, Card, CardHeader, Code, ErrorState, Spinner } from "@/components/ui";
import { errorText } from "@/lib/errors";
import { formatBytes } from "@/lib/format";

const toolNames: Record<string, string> = { composer: "Composer", npm: "npm", yarn: "Yarn", pnpm: "pnpm", pip: "pip", uv: "uv", gomod: "Go modules", gobuild: "Go build cache", bundler: "Bundler" };

/** The package cache every project shares: what each tool keeps there, and emptying it. */
export function PackageCacheCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["package-cache"], queryFn: async () => (await api.packageCache.get()).cache });
  const [msg, setMsg] = useState<{ tone: "green" | "red"; text: string } | null>(null);
  const clear = useMutation({
    mutationFn: async (tool: string) => (await api.packageCache.clear(tool)).cache,
    onSuccess: (cache, tool) => {
      const freed = (q.data?.bytes ?? 0) - cache.bytes;
      qc.setQueryData(["package-cache"], cache);
      setMsg({ tone: "green", text: tool ? t("{{tool}} cache emptied, {{size}} freed.", { tool: t(toolNames[tool] ?? tool), size: formatBytes(freed) }) : t("Package cache emptied, {{size}} freed.", { size: formatBytes(freed) }) });
    },
    onError: (err) => setMsg({ tone: "red", text: errorText(err, t, t("Emptying the cache failed")) }),
  });

  return (
    <Card>
      <CardHeader
        title={
          <span className="flex items-center gap-2">
            <Package className="size-4 text-accent-500" aria-hidden />
            {t("Package cache")}
          </span>
        }
        description={t("Composer, npm, Yarn, pip, uv, Go and Bundler keep their downloads in one cache shared by all projects, so a package is downloaded once. It fills up over time; emptying it only means the next install downloads again.")}
      />
      <div className="space-y-4 p-5">
        {msg && <Alert tone={msg.tone}>{msg.text}</Alert>}
        {q.isPending ? (
          <Spinner />
        ) : q.isError ? (
          <ErrorState message={errorText(q.error, t)} />
        ) : (
          <>
            <p className="text-sm">
              {t("In use: {{size}}", { size: formatBytes(q.data.bytes) })} <span className="text-xs text-subtle">· <Code>{q.data.path}</Code></span>
            </p>
            {q.data.entries.length > 0 && (
              <ul className="divide-y divide-[var(--border)] rounded-md border border-default">
                {q.data.entries.map((e) => (
                  <li key={e.tool} className="flex items-center justify-between gap-3 px-3 py-2 text-sm">
                    <span>{t(toolNames[e.tool] ?? e.tool)}</span>
                    <span className="flex items-center gap-2">
                      <span className="tabular-nums text-muted">{formatBytes(e.bytes)}</span>
                      <Button variant="ghost" size="sm" aria-label={t("Empty the {{tool}} cache", { tool: t(toolNames[e.tool] ?? e.tool) })} disabled={clear.isPending || e.bytes === 0} onClick={() => { setMsg(null); clear.mutate(e.tool); }}>
                        <Trash2 className="size-4" />
                      </Button>
                    </span>
                  </li>
                ))}
              </ul>
            )}
            <Button icon={<Trash2 className="size-4" />} loading={clear.isPending} disabled={q.data.bytes === 0} onClick={() => { setMsg(null); clear.mutate(""); }}>
              {t("Empty the whole cache")}
            </Button>
            <p className="text-xs text-subtle">{t("An install running while the cache is emptied may fail; run it again.")}</p>
          </>
        )}
      </div>
    </Card>
  );
}
