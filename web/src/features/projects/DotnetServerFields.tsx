import { defaultDotnetPresets, type DotnetPreset } from "@/api/types";
import { useTranslation } from "react-i18next";
import { Checkbox, Field, Input, Select } from "@/components/ui";

export interface DotnetServerForm {
  server: boolean;
  /** "dev" (dotnet watch) or "production" (publish once, run the DLL). */
  mode: string;
  /** "aspnetcore" or "dll". */
  preset: string;
  /** Project file to run; empty finds the one project at the top, else the one web or worker project. */
  project: string;
  /** The DLL the "dll" preset runs; empty runs what the publish produced. */
  dll: string;
  port: string;
}

export const defaultDotnetServerForm: DotnetServerForm = { server: false, mode: "dev", preset: "aspnetcore", project: "", dll: "", port: "8080" };

/** Converts the form into the request fields (numbers parsed, defaults applied server-side). */
export function dotnetServerRequest(f: DotnetServerForm): { server: boolean; mode: string; preset: string; project: string; dll: string; port: number } {
  return {
    server: f.server,
    mode: f.mode,
    preset: f.preset,
    project: f.project.trim(),
    dll: f.preset === "dll" ? f.dll.trim() : "",
    port: Number(f.port) || 0,
  };
}

/** What the backend runs for a preset and mode (runtime.DotnetConfig.Command), shown so the choice is clear. */
export function dotnetCommandHint(preset: string, mode: string, project: string, dll: string): string {
  const proj = project || "<project>.csproj";
  if (mode !== "production" && preset === "aspnetcore") return `dotnet watch --project ${proj} run`;
  return `dotnet publish ${proj}, then dotnet ${dll || "bin/envoryx-publish/<app>.dll"}`;
}

export function DotnetServerFields({
  value,
  onChange,
  idPrefix = "dotnet",
  presets = defaultDotnetPresets,
  primary = false,
}: {
  value: DotnetServerForm;
  onChange: (v: DotnetServerForm) => void;
  idPrefix?: string;
  /** From GET /runtimes (dotnetPresets); the built-in list stands in for older backends. */
  presets?: DotnetPreset[];
  /** The server is the project's application (no PHP, Python, Go, Ruby or Java server): it answers on the project URL. */
  primary?: boolean;
}) {
  const { t } = useTranslation();
  const set = (patch: Partial<DotnetServerForm>) => onChange({ ...value, ...patch });
  const current = presets.find((p) => p.key === value.preset);
  const choosePreset = (key: string) => {
    const next = presets.find((p) => p.key === key);
    // Follow the preset's default port only while the user has not edited it.
    const portUntouched = current !== undefined && value.port === String(current.port);
    set({ preset: key, port: portUntouched && next ? String(next.port) : value.port });
  };
  const hasDevMode = value.preset !== "dll";
  return (
    <div className="space-y-3">
      <Checkbox
        label={t("Run the .NET server")}
        description={
          primary
            ? t("The server becomes the container's main process (restarted automatically) and answers on the project URL plus a direct host port. It needs a .csproj - use a .NET template, clone a repository or create the project in the .NET terminal.")
            : t("The server becomes the container's main process (restarted automatically) on a direct host port; the project URL keeps reaching PHP, Python, Go, Ruby or Java.")
        }
        checked={value.server}
        onChange={(e) => set({ server: e.target.checked })}
      />
      {value.server && hasDevMode && (
        <fieldset className="space-y-2">
          <legend className="text-sm font-medium text-fg">{t("Mode")}</legend>
          <div className="grid gap-2 sm:grid-cols-2">
            {[
              { key: "dev", label: t("Development"), text: t("dotnet watch: code changes are applied with hot reload, the application restarts when they can't be.") },
              { key: "production", label: t("Production server"), text: t("Publishes the project once when the container starts, then runs the DLL - what a deployment runs.") },
            ].map((m) => (
              <label key={m.key} className={`flex cursor-pointer items-start gap-2 rounded-md border px-3 py-2 text-sm ${value.mode === m.key ? "border-accent-500 bg-accent-500/5" : "border-default"}`}>
                <input type="radio" name={`${idPrefix}-mode`} className="mt-0.5 accent-accent-600" checked={value.mode === m.key} onChange={() => set({ mode: m.key })} aria-label={m.label} />
                <span>
                  <span className="block font-medium text-fg">{m.label}</span>
                  <span className="block text-xs text-muted">{m.text}</span>
                </span>
              </label>
            ))}
          </div>
        </fieldset>
      )}
      {value.server && (
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t("Framework preset")} htmlFor={`${idPrefix}-preset`} hint={t("Decides which server command runs.")}>
            <Select id={`${idPrefix}-preset`} value={value.preset} onChange={(e) => choosePreset(e.target.value)}>
              {presets.map((p) => (
                <option key={p.key} value={p.key}>{t(p.label)}</option>
              ))}
            </Select>
          </Field>
          <Field label={t("Port inside the container")} htmlFor={`${idPrefix}-port`} hint={current ? t("Default for {{preset}}: {{port}}", { preset: t(current.label), port: current.port }) : undefined}>
            <Input id={`${idPrefix}-port`} type="number" min={1024} max={65535} value={value.port} onChange={(e) => set({ port: e.target.value })} />
          </Field>
          <Field label={t("Project file")} htmlFor={`${idPrefix}-project`} hint={t("Relative to the project, e.g. src/Shop/Shop.csproj. Empty takes the one project file at the top, else the one web or worker project below it.")}>
            <Input id={`${idPrefix}-project`} value={value.project} placeholder="src/Shop/Shop.csproj" onChange={(e) => set({ project: e.target.value })} className="font-mono text-xs" />
          </Field>
          {value.preset === "dll" && (
            <Field label={t("DLL")} htmlFor={`${idPrefix}-dll`} hint={t("Relative to the project. Empty runs the application the publish leaves in bin/envoryx-publish/.")}>
              <Input id={`${idPrefix}-dll`} value={value.dll} placeholder="bin/envoryx-publish/Worker.dll" onChange={(e) => set({ dll: e.target.value })} className="font-mono text-xs" />
            </Field>
          )}
          <Field label={t("Command")} htmlFor={`${idPrefix}-command`} hint={t("Runs from the project directory; ASPNETCORE_HTTP_PORTS (or --urls under dotnet watch) and PORT carry the port.")}>
            <Input id={`${idPrefix}-command`} value={dotnetCommandHint(value.preset, value.mode, value.project, value.dll)} readOnly className="font-mono text-xs" />
          </Field>
        </div>
      )}
      <p className="text-xs text-subtle">{t("Debugging needs no port: VS Code starts netcoredbg in the container over SSH, Rider attaches over SSH. The IDE tab has the setup.")}</p>
    </div>
  );
}
