import { Download, Eye, EyeOff, FileUp, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { EnvVar } from "@/api/types";
import { Button, Input } from "@/components/ui";
import { serializeDotenv } from "@/lib/dotenv";
import { EnvImportDialog } from "./EnvImportDialog";

/**
 * The project's variables. services are the project's (or the wizard's) services, so an
 * imported .env can tell which variables Envoryx sets itself; exportName names the
 * downloaded file.
 */
export function EnvEditor({
  value,
  onChange,
  services = [],
  exportName = "project",
}: {
  value: EnvVar[];
  onChange: (next: EnvVar[]) => void;
  services?: { kind: string; enabled?: boolean; variant?: string }[];
  exportName?: string;
}) {
  const { t } = useTranslation();
  const [importing, setImporting] = useState(false);
  const merge = (vars: EnvVar[]) => {
    const next = [...value];
    for (const v of vars) {
      const i = next.findIndex((e) => e.key === v.key);
      if (i >= 0) next[i] = v;
      else next.push(v);
    }
    onChange(next);
  };
  const download = () => {
    const text = serializeDotenv(
      value.filter((e) => e.key),
      t("Environment of {{project}}, exported by Envoryx. It holds secrets in plain text; the variables Envoryx sets for the services are not part of it.", { project: exportName }),
    );
    const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = `${exportName}.env`;
    a.click();
    URL.revokeObjectURL(url);
  };
  const update = (i: number, patch: Partial<EnvVar>) => onChange(value.map((e, idx) => (idx === i ? { ...e, ...patch } : e)));
  const remove = (i: number) => onChange(value.filter((_, idx) => idx !== i));
  const add = () => onChange([...value, { key: "", value: "", isSecret: false }]);

  return (
    <div className="space-y-2">
      {value.length === 0 && <p className="text-sm text-subtle">No environment variables. They are injected into every project container.</p>}
      {value.map((e, i) => (
        <div key={i} className="flex items-center gap-2">
          <Input
            aria-label={t("Variable name")}
            placeholder="APP_ENV"
            value={e.key}
            onChange={(ev) => update(i, { key: ev.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, "") })}
            className="w-44 font-mono text-xs"
            spellCheck={false}
          />
          <Input
            aria-label={t("Variable value")}
            placeholder="value"
            type={e.isSecret ? "password" : "text"}
            value={e.value}
            onChange={(ev) => update(i, { value: ev.target.value })}
            className="font-mono text-xs"
            spellCheck={false}
            autoComplete="off"
          />
          <Button variant="ghost" size="sm" onClick={() => update(i, { isSecret: !e.isSecret })} title={e.isSecret ? t("Secret (masked)") : t("Mark as secret")} aria-label={t("Toggle secret")}>
            {e.isSecret ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
          </Button>
          <Button variant="ghost" size="sm" onClick={() => remove(i)} aria-label={t("Remove variable")}>
            <Trash2 className="size-4" />
          </Button>
        </div>
      ))}
      <div className="flex flex-wrap gap-2">
        <Button size="sm" onClick={add} icon={<Plus className="size-3.5" />}>
          {t("Add variable")}
        </Button>
        <Button size="sm" variant="ghost" onClick={() => setImporting(true)} icon={<FileUp className="size-3.5" />}>
          {t("Import .env")}
        </Button>
        <Button size="sm" variant="ghost" onClick={download} disabled={!value.some((e) => e.key)} icon={<Download className="size-3.5" />} title={t("Downloads the variables as a .env file, secrets included.")}>
          {t("Export .env")}
        </Button>
      </div>
      <EnvImportDialog open={importing} onClose={() => setImporting(false)} current={value} services={services} onImport={merge} />
    </div>
  );
}
