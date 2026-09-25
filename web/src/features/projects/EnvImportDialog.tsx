import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Eye, EyeOff, FileUp } from "lucide-react";
import type { EnvVar } from "@/api/types";
import { Badge, Button, Dialog, Field } from "@/components/ui";
import { looksSecret, parseDotenv } from "@/lib/dotenv";
import { injectedEnv, reservedEnvKey } from "@/lib/injectedEnv";
import { serviceLabel } from "@/lib/format";

type Status = "new" | "changed" | "same" | "injected" | "invalid";

interface Row {
  key: string;
  value: string;
  line: number;
  status: Status;
  /** Why a variable cannot be imported, or which service sets it. */
  reason?: string | undefined;
  include: boolean;
  secret: boolean;
}

const nameRe = /^[A-Z_][A-Z0-9_]*$/;

/**
 * Reads a .env file (pasted or chosen) into the project's variables: new ones are added,
 * changed ones replace the current value, and the ones Envoryx sets itself for a service
 * are left out unless picked – the old setup's DB_HOST=127.0.0.1 would otherwise win over
 * the project database.
 */
export function EnvImportDialog({
  open,
  onClose,
  current,
  services,
  onImport,
}: {
  open: boolean;
  onClose: () => void;
  current: EnvVar[];
  services: { kind: string; enabled?: boolean; variant?: string }[];
  onImport: (vars: EnvVar[]) => void;
}) {
  const { t } = useTranslation();
  const [text, setText] = useState("");
  const [picked, setPicked] = useState<Record<string, { include?: boolean; secret?: boolean }>>({});
  const [readError, setReadError] = useState<string | null>(null);

  const parsed = useMemo(() => parseDotenv(text), [text]);
  const rows = useMemo<Row[]>(() => {
    const injected = injectedEnv(services);
    const have = new Map(current.map((e) => [e.key, e]));
    return parsed.entries.map((e) => {
      const existing = have.get(e.key);
      let status: Status = existing ? (existing.value === e.value ? "same" : "changed") : "new";
      let reason: string | undefined;
      if (!nameRe.test(e.key)) {
        status = "invalid";
        reason = t("Names are upper-case letters, digits and underscores.");
      } else if (reservedEnvKey(e.key)) {
        status = "invalid";
        reason = t("Reserved for Envoryx and its containers.");
      } else if (e.value.includes("\n") || e.value.includes("\r")) {
        status = "invalid";
        reason = t("Values with line breaks are not supported; keep such a value in a file.");
      } else if (e.value.length > 4096) {
        status = "invalid";
        reason = t("Longer than 4096 characters.");
      } else if (injected.has(e.key) && !existing) {
        status = "injected";
        const kind = injected.get(e.key)!;
        const svc = services.find((s) => s.kind === kind);
        reason = serviceLabel(kind, undefined, svc?.variant);
      }
      const defaults = { include: status === "new" || status === "changed", secret: existing?.isSecret || looksSecret(e.key, e.value) };
      const p = picked[e.key] ?? {};
      return { ...e, status, reason, include: status !== "invalid" && (p.include ?? defaults.include), secret: p.secret ?? defaults.secret };
    });
  }, [parsed, current, services, picked, t]);

  const chosen = rows.filter((r) => r.include);
  const close = () => {
    setText("");
    setPicked({});
    setReadError(null);
    onClose();
  };
  const readFile = async (file: File | undefined) => {
    if (!file) return;
    setReadError(null);
    if (file.size > 1 << 20) {
      setReadError(t("The file is larger than 1 MB – is it really a .env file?"));
      return;
    }
    setText(await file.text());
    setPicked({});
  };

  const badge = (r: Row) => {
    switch (r.status) {
      case "new":
        return <Badge tone="green">{t("new")}</Badge>;
      case "changed":
        return <Badge tone="amber">{t("replaces the current value")}</Badge>;
      case "same":
        return <Badge>{t("unchanged")}</Badge>;
      case "injected":
        return <Badge tone="blue">{t("set by Envoryx ({{service}})", { service: r.reason ?? "" })}</Badge>;
      default:
        return <Badge tone="red">{r.reason}</Badge>;
    }
  };

  return (
    <Dialog
      open={open}
      onClose={close}
      title={t("Import a .env file")}
      description={t("Paste the file or choose it. Nothing is saved until you save the variables.")}
      footer={
        <>
          <Button onClick={close}>{t("Cancel")}</Button>
          <Button
            variant="primary"
            disabled={chosen.length === 0}
            onClick={() => {
              onImport(chosen.map((r) => ({ key: r.key, value: r.value, isSecret: r.secret })));
              close();
            }}
          >
            {t("Import {{count}} variables", { count: chosen.length })}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <div className="flex flex-wrap items-center gap-2">
          <label className="inline-flex cursor-pointer items-center gap-2 rounded-md border border-default px-3 py-1.5 text-sm hover:bg-muted">
            <FileUp className="size-4" aria-hidden />
            {t("Choose a file")}
            <input type="file" className="sr-only" aria-label={t("Choose a file")} accept=".env,.txt,text/plain" onChange={(e) => void readFile(e.target.files?.[0])} />
          </label>
          {readError && <span className="text-xs text-red-600 dark:text-red-400">{readError}</span>}
        </div>
        <Field label={t(".env contents")} htmlFor="env-import-text">
          <textarea
            id="env-import-text"
            value={text}
            onChange={(e) => {
              setText(e.target.value);
              setPicked({});
            }}
            rows={6}
            spellCheck={false}
            className="w-full rounded-md border border-default bg-elevated p-2 font-mono text-[11px] text-fg focus:border-accent-500 focus:outline-none"
            placeholder={"APP_NAME=Shop\nAPP_KEY=base64:…"}
          />
        </Field>
        {parsed.problems.length > 0 && (
          <p className="text-xs text-amber-700 dark:text-amber-400">
            {t("Lines that are no variable were skipped: {{lines}}", { lines: parsed.problems.map((p) => p.line).join(", ") })}
          </p>
        )}
        {rows.length > 0 && (
          <ul className="max-h-80 divide-y divide-[var(--border)] overflow-y-auto rounded-md border border-default">
            {rows.map((r) => (
              <li key={r.key} className="flex items-center gap-3 px-3 py-2 text-xs">
                <input
                  type="checkbox"
                  className="size-4 accent-accent-600"
                  aria-label={t("Import {{name}}", { name: r.key })}
                  checked={r.include}
                  disabled={r.status === "invalid"}
                  onChange={(e) => setPicked({ ...picked, [r.key]: { ...picked[r.key], include: e.target.checked } })}
                />
                <span className="w-44 shrink-0 truncate font-mono font-medium" title={r.key}>
                  {r.key}
                </span>
                <span className="min-w-0 flex-1 truncate font-mono text-muted" title={r.secret ? undefined : r.value}>
                  {r.secret ? "•".repeat(Math.min(Math.max(r.value.length, 4), 16)) : r.value || t("(empty)")}
                </span>
                {badge(r)}
                <Button
                  variant="ghost"
                  size="sm"
                  aria-label={t("Secret {{name}}", { name: r.key })}
                  title={r.secret ? t("Secret (masked)") : t("Mark as secret")}
                  aria-pressed={r.secret}
                  onClick={() => setPicked({ ...picked, [r.key]: { ...picked[r.key], secret: !r.secret } })}
                >
                  {r.secret ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                </Button>
              </li>
            ))}
          </ul>
        )}
        {rows.some((r) => r.status === "injected") && (
          <p className="text-xs text-muted">
            {t("Variables marked “set by Envoryx” are injected for the project's services and point at them. Importing one replaces that value – leave them out unless the application really has to reach something else.")}
          </p>
        )}
      </div>
    </Dialog>
  );
}
