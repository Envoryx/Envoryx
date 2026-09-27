import { defaultJavaPresets, type JavaPreset } from "@/api/types";
import { useTranslation } from "react-i18next";
import { Checkbox, Field, Input, Select } from "@/components/ui";

export interface JavaServerForm {
  server: boolean;
  /** "dev" (the framework's dev mode) or "production" (build once, run the jar). */
  mode: string;
  /** "spring-boot", "quarkus" or "jar". */
  preset: string;
  /** The jar the "jar" preset runs; empty picks the newest the build left behind. */
  jar: string;
  port: string;
  debug: boolean;
  debugPort: string;
}

export const defaultJavaServerForm: JavaServerForm = { server: false, mode: "dev", preset: "spring-boot", jar: "", port: "8080", debug: false, debugPort: "5005" };

/** Converts the form into the request fields (numbers parsed, defaults applied server-side). */
export function javaServerRequest(f: JavaServerForm): { server: boolean; mode: string; preset: string; jar: string; port: number; debug: boolean; debugPort: number } {
  return {
    server: f.server,
    mode: f.mode,
    preset: f.preset,
    jar: f.preset === "jar" ? f.jar.trim() : "",
    port: Number(f.port) || 0,
    debug: f.debug,
    debugPort: f.debug ? Number(f.debugPort) || 0 : 0,
  };
}

/** What the backend runs for a preset and mode (runtime.JavaConfig.Command), shown so the choice is clear. */
export function javaCommandHint(preset: string, mode: string, jar: string): string {
  if (mode !== "production" && preset === "spring-boot") return "mvn spring-boot:run | gradle bootRun";
  if (mode !== "production" && preset === "quarkus") return "mvn quarkus:dev | gradle quarkusDev";
  const run = preset === "quarkus" ? "java -jar target/quarkus-app/quarkus-run.jar" : `java -jar ${jar || "target/*.jar | build/libs/*.jar"}`;
  return `mvn package | gradle build, then ${run}`;
}

export function JavaServerFields({
  value,
  onChange,
  idPrefix = "java",
  presets = defaultJavaPresets,
  primary = false,
}: {
  value: JavaServerForm;
  onChange: (v: JavaServerForm) => void;
  idPrefix?: string;
  /** From GET /runtimes (javaPresets); the built-in list stands in for older backends. */
  presets?: JavaPreset[];
  /** The server is the project's application (no PHP, Python, Go or Ruby server): it answers on the project URL. */
  primary?: boolean;
}) {
  const { t } = useTranslation();
  const set = (patch: Partial<JavaServerForm>) => onChange({ ...value, ...patch });
  const current = presets.find((p) => p.key === value.preset);
  const choosePreset = (key: string) => {
    const next = presets.find((p) => p.key === key);
    // Follow the preset's default port only while the user has not edited it.
    const portUntouched = current !== undefined && value.port === String(current.port);
    set({ preset: key, port: portUntouched && next ? String(next.port) : value.port });
  };
  const hasDevMode = value.preset !== "jar";
  return (
    <div className="space-y-3">
      <Checkbox
        label={t("Run the Java server")}
        description={
          primary
            ? t("The server becomes the container's main process (restarted automatically) and answers on the project URL plus a direct host port. It needs a pom.xml or build.gradle - use a Java template, clone a repository or create the project in the Java terminal. Maven or Gradle follows the project, and its wrapper wins.")
            : t("The server becomes the container's main process (restarted automatically) on a direct host port; the project URL keeps reaching PHP, Python, Go or Ruby.")
        }
        checked={value.server}
        onChange={(e) => set({ server: e.target.checked })}
      />
      {value.server && hasDevMode && (
        <fieldset className="space-y-2">
          <legend className="text-sm font-medium text-fg">{t("Mode")}</legend>
          <div className="grid gap-2 sm:grid-cols-2">
            {[
              { key: "dev", label: t("Development"), text: t("The framework's dev mode: Quarkus recompiles on the next request, Spring Boot DevTools restarts when compiled classes change.") },
              { key: "production", label: t("Production server"), text: t("Builds the project once when the container starts, then runs the jar - what a deployment runs.") },
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
          {value.preset === "jar" && (
            <Field label={t("Jar")} htmlFor={`${idPrefix}-jar`} hint={t("Relative to the project, e.g. build/libs/app.jar. Empty runs the newest jar the build leaves in target/ or build/libs/.")}>
              <Input id={`${idPrefix}-jar`} value={value.jar} placeholder="target/app.jar" onChange={(e) => set({ jar: e.target.value })} className="font-mono text-xs" />
            </Field>
          )}
          <Field label={t("Command")} htmlFor={`${idPrefix}-command`} hint={t("Runs from the project directory; SERVER_PORT, QUARKUS_HTTP_PORT and PORT carry the port.")}>
            <Input id={`${idPrefix}-command`} value={javaCommandHint(value.preset, value.mode, value.jar)} readOnly className="font-mono text-xs" />
          </Field>
        </div>
      )}
      <div className="space-y-3">
        <Checkbox
          label={t("Debug with JDWP")}
          description={
            value.server
              ? t("The server's JVM starts with a JDWP agent that IntelliJ IDEA (Run → Remote JVM Debug) and VS Code attach to. The IDE tab has the connection.")
              : t("Publishes the JDWP port for a JVM started in the Java terminal with -agentlib:jdwp=transport=dt_socket,server=y,suspend=n,address=*:5005. The IDE tab has the connection.")
          }
          checked={value.debug}
          onChange={(e) => set({ debug: e.target.checked })}
        />
        {value.debug && <p className="text-xs text-amber-700 dark:text-amber-300">{t("JDWP accepts everyone who reaches the port and can run any code in the JVM. Switch it off when you are not debugging.")}</p>}
        {value.debug && (
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label={t("JDWP port inside the container")} htmlFor={`${idPrefix}-debug-port`} hint={t("IntelliJ's Remote JVM Debug starts with 5005")}>
              <Input id={`${idPrefix}-debug-port`} type="number" min={1024} max={65535} value={value.debugPort} onChange={(e) => set({ debugPort: e.target.value })} />
            </Field>
          </div>
        )}
      </div>
    </div>
  );
}
