import { useTranslation } from "react-i18next";
import { Checkbox, Field, Input } from "@/components/ui";

export interface GoServerForm {
  server: boolean;
  /** "dev" (air rebuilds on every change) or "production" (one build, then the binary). */
  mode: string;
  /** The main package, "." or a path like ./cmd/server. */
  pkg: string;
  port: string;
  debug: boolean;
  debugPort: string;
}

export const defaultGoServerForm: GoServerForm = { server: false, mode: "dev", pkg: ".", port: "8080", debug: false, debugPort: "2345" };

/** Converts the form into the request fields (numbers parsed, defaults applied server-side). */
export function goServerRequest(f: GoServerForm): { server: boolean; mode: string; package: string; port: number; debug: boolean; debugPort: number } {
  return {
    server: f.server,
    mode: f.mode,
    package: f.pkg.trim(),
    port: Number(f.port) || 0,
    debug: f.debug,
    debugPort: f.debug ? Number(f.debugPort) || 0 : 0,
  };
}

/** What the backend runs for a mode (runtime.GoConfig.Command) – shown so the choice is clear. */
export function goCommandHint(mode: string, pkg: string, debug: boolean): string {
  const p = pkg.trim() || ".";
  const build = `go build${debug ? " -gcflags='all=-N -l'" : ""} ${p}`;
  if (mode === "production") return debug ? `${build} && dlv exec --headless …` : `${build} && ./app`;
  return debug ? `air (${build}, run under dlv)` : `air (${build})`;
}

export function GoServerFields({ value, onChange, idPrefix = "go", primary = false }: { value: GoServerForm; onChange: (v: GoServerForm) => void; idPrefix?: string; primary?: boolean }) {
  const { t } = useTranslation();
  const set = (patch: Partial<GoServerForm>) => onChange({ ...value, ...patch });
  return (
    <div className="space-y-3">
      <Checkbox
        label={t("Build and run the server")}
        description={
          primary
            ? t("The server becomes the container's main process (restarted automatically) and answers on the project URL plus a direct host port. It needs a go.mod – use a Go template, clone a repository or run go mod init in the Go terminal.")
            : t("The server becomes the container's main process (restarted automatically) on a direct host port; the project URL keeps reaching PHP or Python.")
        }
        checked={value.server}
        onChange={(e) => set({ server: e.target.checked })}
      />
      {value.server && (
        <fieldset className="space-y-2">
          <legend className="text-sm font-medium text-fg">{t("Mode")}</legend>
          <div className="grid gap-2 sm:grid-cols-2">
            {[
              { key: "dev", label: t("Development"), text: t("air rebuilds and restarts the server whenever a .go file changes; a .air.toml in the project takes over.") },
              { key: "production", label: t("Production build"), text: t("One go build when the container starts, then the binary – restart the project after changes.") },
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
          <Field label={t("Main package")} htmlFor={`${idPrefix}-package`} hint={t(". for the module root or a path like ./cmd/server")}>
            <Input id={`${idPrefix}-package`} value={value.pkg} onChange={(e) => set({ pkg: e.target.value })} placeholder="." spellCheck={false} />
          </Field>
          <Field label={t("Port inside the container")} htmlFor={`${idPrefix}-port`} hint={t("Your server reads it from $PORT (default 8080).")}>
            <Input id={`${idPrefix}-port`} type="number" min={1024} max={65535} value={value.port} onChange={(e) => set({ port: e.target.value })} />
          </Field>
          <Field label={t("Command")} htmlFor={`${idPrefix}-command`} hint={t("Builds into the container, never into the project directory.")}>
            <Input id={`${idPrefix}-command`} value={goCommandHint(value.mode, value.pkg, value.debug)} readOnly className="font-mono text-xs" />
          </Field>
        </div>
      )}
      <div className="space-y-3">
        <Checkbox
          label={t("Debug with Delve")}
          description={
            value.server
              ? t("The server runs under a headless Delve that GoLand or VS Code attach to; breakpoints work after every rebuild. The IDE tab has the connection.")
              : t("Publishes the Delve port for a dlv started in the Go terminal, e.g. dlv test --headless --listen=:2345 ./pkg/... The IDE tab has the connection.")
          }
          checked={value.debug}
          onChange={(e) => set({ debug: e.target.checked })}
        />
        {value.debug && (
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label={t("Delve port inside the container")} htmlFor={`${idPrefix}-debug-port`} hint={t("Delve's usual port is 2345")}>
              <Input id={`${idPrefix}-debug-port`} type="number" min={1024} max={65535} value={value.debugPort} onChange={(e) => set({ debugPort: e.target.value })} />
            </Field>
          </div>
        )}
      </div>
    </div>
  );
}
