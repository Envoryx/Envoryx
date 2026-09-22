import { defaultNodePresets, type NodePreset } from "@/api/types";
import { useTranslation } from "react-i18next";
import { Checkbox, Field, Input, Select } from "@/components/ui";

export interface DevServerForm {
  devServer: boolean;
  /** "dev" or "production" (build, then serve with NODE_ENV=production). */
  mode: string;
  packageManager: string;
  script: string;
  buildScript: string;
  port: string;
  preset: string;
  inspect: boolean;
  inspectPort: string;
}

export const defaultDevServerForm: DevServerForm = { devServer: false, mode: "dev", packageManager: "npm", script: "dev", buildScript: "build", port: "5173", preset: "vite", inspect: false, inspectPort: "9229" };

/** The script the backend defaults to for a mode and preset; the form mirrors it so the field never shows a stale value. */
export function defaultScript(mode: string, preset: string): string {
  if (mode !== "production") return "dev";
  return preset === "vite" ? "preview" : "start";
}

/** Converts the form into the request fields (numbers parsed, defaults applied server-side). */
export function devServerRequest(f: DevServerForm): { devServer: boolean; mode: string; packageManager: string; script: string; buildScript: string; port: number; preset: string; inspect: boolean; inspectPort: number } {
  return {
    devServer: f.devServer,
    mode: f.mode,
    packageManager: f.packageManager,
    script: f.script.trim(),
    buildScript: f.mode === "production" ? f.buildScript.trim() : "",
    port: Number(f.port) || 0,
    preset: f.preset,
    inspect: f.inspect,
    inspectPort: f.inspect ? Number(f.inspectPort) || 0 : 0,
  };
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
    // Follow the framework's default port and script only while the user has not edited them.
    const untouched = current !== undefined && value.port === String(current.port);
    const scriptUntouched = value.script === defaultScript(value.mode, value.preset);
    set({ preset: key, port: untouched && next ? String(next.port) : value.port, script: scriptUntouched ? defaultScript(value.mode, key) : value.script });
  };
  const chooseMode = (mode: string) => {
    const scriptUntouched = value.script === defaultScript(value.mode, value.preset);
    set({ mode, script: scriptUntouched ? defaultScript(mode, value.preset) : value.script });
  };
  const production = value.mode === "production";
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
        <fieldset className="space-y-2">
          <legend className="text-sm font-medium text-fg">{t("Mode")}</legend>
          <div className="grid gap-2 sm:grid-cols-2">
            {[
              { key: "dev", label: t("Dev server"), text: t("The script runs with hot reload; the container restarts it when it exits.") },
              { key: "production", label: t("Production build"), text: t("Every container start runs the build script, then the serve script with NODE_ENV=production – a production-like run of Next.js, Nuxt or Vite preview.") },
            ].map((m) => (
              <label key={m.key} className={`flex cursor-pointer items-start gap-2 rounded-md border px-3 py-2 text-sm ${value.mode === m.key ? "border-accent-500 bg-accent-500/5" : "border-default"}`}>
                <input type="radio" name={`${idPrefix}-mode`} className="mt-0.5 accent-accent-600" checked={value.mode === m.key} onChange={() => chooseMode(m.key)} aria-label={m.label} />
                <span>
                  <span className="block font-medium text-fg">{m.label}</span>
                  <span className="block text-xs text-muted">{m.text}</span>
                </span>
              </label>
            ))}
          </div>
        </fieldset>
      )}
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
          {production && (
            <Field label={t("Build script")} htmlFor={`${idPrefix}-build`} hint={t("package.json script that produces the build (runs on every start)")}>
              <Input id={`${idPrefix}-build`} value={value.buildScript} onChange={(e) => set({ buildScript: e.target.value })} placeholder="build" spellCheck={false} />
            </Field>
          )}
          <Field label={production ? t("Serve script") : t("Script")} htmlFor={`${idPrefix}-script`} hint={production ? t("package.json script that serves the build (start, preview)") : t("package.json script name")}>
            <Input id={`${idPrefix}-script`} value={value.script} onChange={(e) => set({ script: e.target.value })} placeholder={defaultScript(value.mode, value.preset)} spellCheck={false} />
          </Field>
          <Field label={t("Port inside the container")} htmlFor={`${idPrefix}-port`} hint={current ? t("Default for {{preset}}: {{port}}", { preset: t(current.label), port: current.port }) : undefined}>
            <Input id={`${idPrefix}-port`} type="number" min={1024} max={65535} value={value.port} onChange={(e) => set({ port: e.target.value })} />
          </Field>
        </div>
      )}
      {value.devServer && (
        <div className="space-y-3">
          <Checkbox
            label={t("Publish the Node.js inspector port")}
            description={t("For attaching a debugger from WebStorm or VS Code. Only the port is published: start the inspector in your script, e.g. NODE_OPTIONS='--inspect=0.0.0.0:9229' next dev – set for the whole container it would attach to npm instead of your app. The IDE tab has the details.")}
            checked={value.inspect}
            onChange={(e) => set({ inspect: e.target.checked })}
          />
          {value.inspect && (
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label={t("Inspector port inside the container")} htmlFor={`${idPrefix}-inspect-port`} hint={t("Node's default is 9229")}>
                <Input id={`${idPrefix}-inspect-port`} type="number" min={1024} max={65535} value={value.inspectPort} onChange={(e) => set({ inspectPort: e.target.value })} />
              </Field>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
