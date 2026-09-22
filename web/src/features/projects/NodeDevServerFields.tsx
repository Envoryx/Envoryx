import { defaultNodePresets, type NodePreset } from "@/api/types";
import { useTranslation } from "react-i18next";
import { Checkbox, Field, Input, Select } from "@/components/ui";

export interface DevServerForm {
  devServer: boolean;
  packageManager: string;
  script: string;
  port: string;
  preset: string;
}

export const defaultDevServerForm: DevServerForm = { devServer: false, packageManager: "npm", script: "dev", port: "5173", preset: "vite" };

/** Converts the form into the request fields (numbers parsed, defaults applied server-side). */
export function devServerRequest(f: DevServerForm): { devServer: boolean; packageManager: string; script: string; port: number; preset: string } {
  return { devServer: f.devServer, packageManager: f.packageManager, script: f.script.trim(), port: Number(f.port) || 0, preset: f.preset };
}

export function NodeDevServerFields({
  value,
  onChange,
  idPrefix = "node",
  presets = defaultNodePresets,
  primary = false,
}: {
  value: DevServerForm;
  onChange: (v: DevServerForm) => void;
  idPrefix?: string;
  /** From GET /runtimes (nodePresets); the built-in list stands in for older backends. */
  presets?: NodePreset[];
  /** The dev server is the project's application (no PHP): it answers on the project URL. */
  primary?: boolean;
}) {
  const { t } = useTranslation();
  const set = (patch: Partial<DevServerForm>) => onChange({ ...value, ...patch });
  const current = presets.find((p) => p.key === value.preset);
  const choosePreset = (key: string) => {
    const next = presets.find((p) => p.key === key);
    // Follow the framework's default port only while the user has not edited the port themselves.
    const untouched = current !== undefined && value.port === String(current.port);
    set({ preset: key, port: untouched && next ? String(next.port) : value.port });
  };
  return (
    <div className="space-y-3">
      <Checkbox
        label={t("Run a dev server")}
        description={
          primary
            ? t("The script becomes the container's main process (restarted automatically) and answers on the project URL; <slug>-dev.<base domain> and a direct host port point at it too. Needs a package.json – use a Node template, clone a repository or scaffold from the Node terminal.")
            : t("The script becomes the container's main process (restarted automatically) and is reachable at <slug>-dev.<base domain> through the proxy plus a direct host port.")
        }
        checked={value.devServer}
        onChange={(e) => set({ devServer: e.target.checked })}
      />
      {value.devServer && (
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t("Framework preset")} htmlFor={`${idPrefix}-preset`} hint={t("Decides how host/port are passed to the script.")}>
            <Select id={`${idPrefix}-preset`} value={value.preset} onChange={(e) => choosePreset(e.target.value)}>
              {presets.map((p) => (
                <option key={p.key} value={p.key}>{t(p.label)}</option>
              ))}
            </Select>
          </Field>
          <Field label={t("Package manager")} htmlFor={`${idPrefix}-pm`}>
            <Select id={`${idPrefix}-pm`} value={value.packageManager} onChange={(e) => set({ packageManager: e.target.value })}>
              <option value="npm">npm</option>
              <option value="pnpm">pnpm</option>
              <option value="yarn">yarn</option>
            </Select>
          </Field>
          <Field label={t("Script")} htmlFor={`${idPrefix}-script`} hint={t("package.json script name")}>
            <Input id={`${idPrefix}-script`} value={value.script} onChange={(e) => set({ script: e.target.value })} placeholder="dev" spellCheck={false} />
          </Field>
          <Field label={t("Port inside the container")} htmlFor={`${idPrefix}-port`} hint={current ? t("Default for {{preset}}: {{port}}", { preset: t(current.label), port: current.port }) : undefined}>
            <Input id={`${idPrefix}-port`} type="number" min={1024} max={65535} value={value.port} onChange={(e) => set({ port: e.target.value })} />
          </Field>
        </div>
      )}
    </div>
  );
}
