import { defaultPythonPresets, type PythonPreset } from "@/api/types";
import { useTranslation } from "react-i18next";
import { Checkbox, Field, Input, Select } from "@/components/ui";

export interface PythonServerForm {
  server: boolean;
  /** "dev" (reload + debug) or "production" (gunicorn/uvicorn without reload). */
  mode: string;
  preset: string;
  app: string;
  port: string;
  debug: boolean;
  debugPort: string;
}

export const defaultPythonServerForm: PythonServerForm = { server: false, mode: "dev", preset: "django", app: "config.wsgi:application", port: "8000", debug: false, debugPort: "5678" };

/** Converts the form into the request fields (numbers parsed, defaults applied server-side). */
export function pythonServerRequest(f: PythonServerForm): { server: boolean; mode: string; preset: string; app: string; port: number; debug: boolean; debugPort: number } {
  return {
    server: f.server,
    mode: f.mode,
    preset: f.preset,
    app: f.app.trim(),
    port: Number(f.port) || 0,
    debug: f.debug,
    debugPort: f.debug ? Number(f.debugPort) || 0 : 0,
  };
}

/** The command the backend runs for a preset and mode – shown so the user knows what the choice means. */
export function pythonCommandHint(preset: string, mode: string, app: string, port: string): string {
  const bind = `0.0.0.0:${port || "8000"}`;
  const a = app || "app:app";
  const production = mode === "production";
  switch (preset) {
    case "django":
      return production ? `gunicorn ${a} --bind ${bind}` : `python manage.py runserver ${bind}`;
    case "flask":
      return production ? `gunicorn ${a} --bind ${bind}` : `flask --app ${a} run --host 0.0.0.0 --port ${port || "5000"} --debug`;
    case "asgi":
      return `uvicorn ${a} --host 0.0.0.0 --port ${port || "8000"}${production ? "" : " --reload"}`;
    case "wsgi":
      return `gunicorn ${a} --bind ${bind}${production ? "" : " --reload"}`;
    default:
      return `python -m ${a}`;
  }
}

export function PythonServerFields({
  value,
  onChange,
  idPrefix = "python",
  presets = defaultPythonPresets,
  primary = false,
}: {
  value: PythonServerForm;
  onChange: (v: PythonServerForm) => void;
  idPrefix?: string;
  /** From GET /runtimes (pythonPresets); the built-in list stands in for older backends. */
  presets?: PythonPreset[];
  /** The server is the project's application (no PHP): it answers on the project URL. */
  primary?: boolean;
}) {
  const { t } = useTranslation();
  const set = (patch: Partial<PythonServerForm>) => onChange({ ...value, ...patch });
  const current = presets.find((p) => p.key === value.preset);
  const choosePreset = (key: string) => {
    const next = presets.find((p) => p.key === key);
    // Follow the preset's default port and app only while the user has not edited them.
    const portUntouched = current !== undefined && value.port === String(current.port);
    const appUntouched = current !== undefined && value.app === current.app;
    set({ preset: key, port: portUntouched && next ? String(next.port) : value.port, app: appUntouched && next ? next.app : value.app });
  };
  return (
    <div className="space-y-3">
      <Checkbox
        label={t("Run the application server")}
        description={
          primary
            ? t("The server becomes the container's main process (restarted automatically) and answers on the project URL plus a direct host port. Needs your application and its .venv – use a Python template, clone a repository or set it up from the Python terminal.")
            : t("The server becomes the container's main process (restarted automatically) on a direct host port; the project URL keeps reaching PHP.")
        }
        checked={value.server}
        onChange={(e) => set({ server: e.target.checked })}
      />
      {value.server && (
        <fieldset className="space-y-2">
          <legend className="text-sm font-medium text-fg">{t("Mode")}</legend>
          <div className="grid gap-2 sm:grid-cols-2">
            {[
              { key: "dev", label: t("Development"), text: t("Auto-reload and debug pages: manage.py runserver, flask run --debug, uvicorn --reload.") },
              { key: "production", label: t("Production server"), text: t("gunicorn (Django, Flask, WSGI) or uvicorn without reload – the packages have to be installed in the .venv.") },
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
          <Field label={current ? t(current.appLabel) : t("Application")} htmlFor={`${idPrefix}-app`} hint={current ? t(current.appHint) : undefined}>
            <Input id={`${idPrefix}-app`} value={value.app} onChange={(e) => set({ app: e.target.value })} placeholder={current?.app ?? "app:app"} spellCheck={false} />
          </Field>
          <Field label={t("Port inside the container")} htmlFor={`${idPrefix}-port`} hint={current ? t("Default for {{preset}}: {{port}}", { preset: t(current.label), port: current.port }) : undefined}>
            <Input id={`${idPrefix}-port`} type="number" min={1024} max={65535} value={value.port} onChange={(e) => set({ port: e.target.value })} />
          </Field>
          <Field label={t("Command")} htmlFor={`${idPrefix}-command`} hint={t("Runs from the project directory with .venv/bin first on PATH.")}>
            <Input id={`${idPrefix}-command`} value={pythonCommandHint(value.preset, value.mode, value.app.trim(), value.port)} readOnly className="font-mono text-xs" />
          </Field>
        </div>
      )}
      <div className="space-y-3">
        <Checkbox
          label={t("Publish the debugpy port")}
          description={t("For attaching a debugger from PyCharm or VS Code. Only the port is published: start debugpy in your application, e.g. python -m debugpy --listen 0.0.0.0:5678 manage.py runserver. The IDE tab has the details.")}
          checked={value.debug}
          onChange={(e) => set({ debug: e.target.checked })}
        />
        {value.debug && (
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label={t("debugpy port inside the container")} htmlFor={`${idPrefix}-debug-port`} hint={t("debugpy's default is 5678")}>
              <Input id={`${idPrefix}-debug-port`} type="number" min={1024} max={65535} value={value.debugPort} onChange={(e) => set({ debugPort: e.target.value })} />
            </Field>
          </div>
        )}
      </div>
    </div>
  );
}
