import { defaultRubyPresets, type RubyPreset } from "@/api/types";
import { useTranslation } from "react-i18next";
import { Checkbox, Field, Input, Select } from "@/components/ui";

export interface RubyServerForm {
  server: boolean;
  /** "dev" (RAILS_ENV/RACK_ENV development) or "production" (the production environment on Puma). */
  mode: string;
  /** "rails" or "rack". */
  preset: string;
  port: string;
  debug: boolean;
  debugPort: string;
}

export const defaultRubyServerForm: RubyServerForm = { server: false, mode: "dev", preset: "rails", port: "3000", debug: false, debugPort: "12345" };

/** Converts the form into the request fields (numbers parsed, defaults applied server-side). */
export function rubyServerRequest(f: RubyServerForm): { server: boolean; mode: string; preset: string; port: number; debug: boolean; debugPort: number } {
  return {
    server: f.server,
    mode: f.mode,
    preset: f.preset,
    port: Number(f.port) || 0,
    debug: f.debug,
    debugPort: f.debug ? Number(f.debugPort) || 0 : 0,
  };
}

/** What the backend runs for a preset and mode (runtime.RubyConfig.Command) – shown so the choice is clear. */
export function rubyCommandHint(preset: string, mode: string, port: string, debug: boolean): string {
  const p = port || (preset === "rack" ? "9292" : "3000");
  const cmd = preset === "rails" && mode !== "production" ? `bin/rails server -b 0.0.0.0 -p ${p}` : `bundle exec puma -b tcp://0.0.0.0:${p}`;
  return debug ? `rdbg --open … -c -- ${cmd}` : cmd;
}

export function RubyServerFields({
  value,
  onChange,
  idPrefix = "ruby",
  presets = defaultRubyPresets,
  primary = false,
}: {
  value: RubyServerForm;
  onChange: (v: RubyServerForm) => void;
  idPrefix?: string;
  /** From GET /runtimes (rubyPresets); the built-in list stands in for older backends. */
  presets?: RubyPreset[];
  /** The server is the project's application (no PHP, Python or Go server): it answers on the project URL. */
  primary?: boolean;
}) {
  const { t } = useTranslation();
  const set = (patch: Partial<RubyServerForm>) => onChange({ ...value, ...patch });
  const current = presets.find((p) => p.key === value.preset);
  const choosePreset = (key: string) => {
    const next = presets.find((p) => p.key === key);
    // Follow the preset's default port only while the user has not edited it.
    const portUntouched = current !== undefined && value.port === String(current.port);
    set({ preset: key, port: portUntouched && next ? String(next.port) : value.port });
  };
  return (
    <div className="space-y-3">
      <Checkbox
        label={t("Run the Ruby server")}
        description={
          primary
            ? t("The server becomes the container's main process (restarted automatically) and answers on the project URL plus a direct host port. It needs a Gemfile – use a Ruby template, clone a repository or run rails new in the Ruby terminal. bundle install runs before every start when gems are missing.")
            : t("The server becomes the container's main process (restarted automatically) on a direct host port; the project URL keeps reaching PHP, Python or Go.")
        }
        checked={value.server}
        onChange={(e) => set({ server: e.target.checked })}
      />
      {value.server && (
        <fieldset className="space-y-2">
          <legend className="text-sm font-medium text-fg">{t("Mode")}</legend>
          <div className="grid gap-2 sm:grid-cols-2">
            {[
              { key: "dev", label: t("Development"), text: t("RAILS_ENV and RACK_ENV development: Rails reloads code on every request, with its error pages.") },
              { key: "production", label: t("Production server"), text: t("RAILS_ENV production on Puma: needs config/master.key or SECRET_KEY_BASE and the assets built with rails assets:precompile.") },
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
          <Field label={t("Command")} htmlFor={`${idPrefix}-command`} hint={t("Runs from the project directory after bundle install; the gems live in the project home.")}>
            <Input id={`${idPrefix}-command`} value={rubyCommandHint(value.preset, value.mode, value.port, value.debug)} readOnly className="font-mono text-xs" />
          </Field>
        </div>
      )}
      <div className="space-y-3">
        <Checkbox
          label={t("Debug with rdbg")}
          description={
            value.server
              ? t("The server runs under rdbg (the debug gem), which VS Code (vscode-rdbg) or rdbg -A in a terminal attach to. RubyMine debugs with its own debugger over the SSH interpreter instead. The IDE tab has the connection.")
              : t("Publishes the rdbg port for an rdbg started in the Ruby terminal, e.g. rdbg --open --host=0.0.0.0 --port=12345 -c -- bin/rails test. The IDE tab has the connection.")
          }
          checked={value.debug}
          onChange={(e) => set({ debug: e.target.checked })}
        />
        {value.debug && <p className="text-xs text-amber-700 dark:text-amber-300">{t("rdbg accepts everyone who reaches the port and can run any code in the container. Switch it off when you are not debugging.")}</p>}
        {value.debug && (
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label={t("rdbg port inside the container")} htmlFor={`${idPrefix}-debug-port`} hint={t("The debug gem's documentation uses 12345")}>
              <Input id={`${idPrefix}-debug-port`} type="number" min={1024} max={65535} value={value.debugPort} onChange={(e) => set({ debugPort: e.target.value })} />
            </Field>
          </div>
        )}
      </div>
    </div>
  );
}
