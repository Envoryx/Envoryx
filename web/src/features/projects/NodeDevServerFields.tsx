import { nodePresets } from "@/api/types";
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

export function NodeDevServerFields({ value, onChange, idPrefix = "node" }: { value: DevServerForm; onChange: (v: DevServerForm) => void; idPrefix?: string }) {
  const { t } = useTranslation();
  const set = (patch: Partial<DevServerForm>) => onChange({ ...value, ...patch });
  return (
    <div className="space-y-3">
      <Checkbox
        label={t("Run a dev server")}
        description={t("The script becomes the container's main process (restarted automatically) and is reachable at <slug>-dev.<base domain> through the proxy plus a direct host port. Run npm install first (Actions).")}
        checked={value.devServer}
        onChange={(e) => set({ devServer: e.target.checked })}
      />
      {value.devServer && (
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t("Framework preset")} htmlFor={`${idPrefix}-preset`} hint={t("Decides how host/port are passed to the script.")}>
            <Select id={`${idPrefix}-preset`} value={value.preset} onChange={(e) => set({ preset: e.target.value, port: e.target.value === "next" && value.port === "5173" ? "3000" : value.port })}>
              {Object.entries(nodePresets).map(([k, v]) => (
                <option key={k} value={k}>{t(v)}</option>
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
          <Field label={t("Port inside the container")} htmlFor={`${idPrefix}-port`} hint="Vite: 5173, Next.js: 3000">
            <Input id={`${idPrefix}-port`} type="number" min={1024} max={65535} value={value.port} onChange={(e) => set({ port: e.target.value })} />
          </Field>
        </div>
      )}
    </div>
  );
}
