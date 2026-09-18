import type { PHPConfig, PHPExtension } from "@/api/types";
import { useTranslation } from "react-i18next";
import { Checkbox, Code, Field, Input, Select } from "@/components/ui";

const sizeOptions = ["64M", "128M", "256M", "512M", "1G", "2G", "-1"];

export function PhpConfigForm({
  value,
  onChange,
  extensions,
  projectDir,
  hostname,
}: {
  value: PHPConfig;
  onChange: (next: PHPConfig) => void;
  extensions: PHPExtension[];
  /** Host path of the project (for the IDE path-mapping hint). */
  projectDir?: string | undefined;
  /** Project host name (PhpStorm server name). */
  hostname?: string | undefined;
}) {
  const { t } = useTranslation();
  const set = <K extends keyof PHPConfig>(key: K, v: PHPConfig[K]) => onChange({ ...value, [key]: v });
  const toggleExt = (name: string, on: boolean) => {
    const next = new Set(value.extensions);
    if (on) next.add(name);
    else next.delete(name);
    set("extensions", [...next].sort());
  };

  return (
    <div className="space-y-6">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="memory_limit" htmlFor="php-memory">
          <Select id="php-memory" value={value.memoryLimit} onChange={(e) => set("memoryLimit", e.target.value)}>
            {sizeOptions.map((o) => (
              <option key={o} value={o}>
                {o === "-1" ? t("Unlimited (-1)") : o}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="max_execution_time" htmlFor="php-exec" hint={t("Seconds, 0 = unlimited")}>
          <Input id="php-exec" type="number" min={0} max={86400} value={value.maxExecutionTime} onChange={(e) => set("maxExecutionTime", Number(e.target.value) || 0)} />
        </Field>
        <Field label="upload_max_filesize" htmlFor="php-upload">
          <Select id="php-upload" value={value.uploadMaxFilesize} onChange={(e) => set("uploadMaxFilesize", e.target.value)}>
            {sizeOptions.filter((o) => o !== "-1").map((o) => (
              <option key={o} value={o}>
                {o}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="post_max_size" htmlFor="php-post">
          <Select id="php-post" value={value.postMaxSize} onChange={(e) => set("postMaxSize", e.target.value)}>
            {sizeOptions.filter((o) => o !== "-1").map((o) => (
              <option key={o} value={o}>
                {o}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="error_reporting" htmlFor="php-err">
          <Select id="php-err" value={value.errorReporting} onChange={(e) => set("errorReporting", e.target.value)}>
            <option value="E_ALL">E_ALL</option>
            <option value="E_ALL & ~E_DEPRECATED & ~E_STRICT">E_ALL & ~E_DEPRECATED & ~E_STRICT</option>
            <option value="E_ALL & ~E_NOTICE">E_ALL & ~E_NOTICE</option>
            <option value="E_ERROR | E_WARNING | E_PARSE">E_ERROR | E_WARNING | E_PARSE</option>
          </Select>
        </Field>
        <div className="flex items-end pb-2">
          <Checkbox label="display_errors" description={t("Show errors in the browser (development)")} checked={value.displayErrors} onChange={(e) => set("displayErrors", e.target.checked)} />
        </div>
      </div>

      <div>
        <p className="mb-2 text-sm font-medium text-fg">{t("Extensions")}</p>
        <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
          {extensions.map((ext) => {
            const checked = ext.builtIn || value.extensions.includes(ext.name);
            const disabled = ext.builtIn || !ext.available;
            return (
              <label
                key={ext.name}
                className={`flex items-start gap-2.5 rounded-md border border-default px-3 py-2 ${disabled ? "opacity-60" : "cursor-pointer hover:bg-muted"}`}
                title={!ext.available ? t("Not available in this PHP image") : ext.builtIn ? t("Built into the image") : undefined}
              >
                <input type="checkbox" className="mt-0.5 size-4 accent-accent-600" checked={checked} disabled={disabled} onChange={(e) => toggleExt(ext.name, e.target.checked)} />
                <span>
                  <span className="block font-mono text-xs text-fg">{ext.name}</span>
                  <span className="block text-[11px] text-subtle">
                    {ext.description}
                    {ext.builtIn ? t(" · built-in") : !ext.available ? t(" · not available") : ""}
                  </span>
                </span>
              </label>
            );
          })}
        </div>
      </div>

      <div className="space-y-3 rounded-md border border-default p-4">
        <Checkbox
          label={t("Xdebug (step debugging)")}
          description={t("Breakpoints in PhpStorm / VS Code. Slows PHP down noticeably – enable only while debugging. Applies to the PHP container after saving.")}
          checked={!!value.xdebug}
          onChange={(e) => set("xdebug", e.target.checked)}
        />
        {value.xdebug && (
          <>
            <div className="grid gap-4 sm:grid-cols-3">
              <Field label={t("Mode")} htmlFor="php-xmode" hint={value.xdebugMode === "trigger" ? t("Only requests with the XDEBUG_TRIGGER cookie/parameter (browser extension) are debugged – no slowdown otherwise.") : t("Every request connects to the IDE; noticeably slower.")}>
                <Select id="php-xmode" value={value.xdebugMode ?? "always"} onChange={(e) => set("xdebugMode", e.target.value as "always" | "trigger")}>
                  <option value="always">{t("Always")}</option>
                  <option value="trigger">{t("Trigger (browser extension)")}</option>
                </Select>
              </Field>
              <Field label={t("IDE key")} htmlFor="php-idekey" hint={t("PHPSTORM (default) or e.g. VSCODE")}>
                <Input id="php-idekey" value={value.xdebugIdeKey ?? "PHPSTORM"} onChange={(e) => set("xdebugIdeKey", e.target.value)} spellCheck={false} />
              </Field>
              <Field label={t("Debugger host (optional)")} htmlFor="php-xhost" hint={t("Your machine's IP. Empty = detected from the request (X-Forwarded-For) or the global setting.")}>
                <Input id="php-xhost" value={value.xdebugClientHost ?? ""} onChange={(e) => set("xdebugClientHost", e.target.value)} placeholder="192.168.1.20" spellCheck={false} />
              </Field>
            </div>
            <details className="text-xs text-muted">
              <summary className="cursor-pointer hover:text-fg">{t("IDE setup")}</summary>
              <ul className="mt-2 list-disc space-y-1 pl-4">
                <li>
                  <span className="font-medium text-fg">PhpStorm:</span> {t("Settings → PHP → Debug: port 9003, “Listen for PHP Debug Connections” on. Settings → PHP → Servers: name/host")} <Code>{hostname ?? "<project host>"}</Code>, {t("“Use path mappings”:")} <Code>{projectDir ?? "<project folder>"}</Code> → <Code>/var/www/html</Code>.
                </li>
                <li>
                  <span className="font-medium text-fg">VS Code</span> ({t("PHP Debug extension")}), <Code>.vscode/launch.json</Code>: <Code>{`{"type":"php","request":"launch","name":"Envoryx","port":9003,"pathMappings":{"/var/www/html":"\${workspaceFolder}"}}`}</Code>
                </li>
                <li>{t("Firewall: port 9003 must be reachable on your machine.")}</li>
                <li>{t("Trigger mode: install “Xdebug helper” (Chrome/Firefox) or JetBrains’ browser extension and switch it to “Debug” on the project tab; CLI:")} <Code>XDEBUG_TRIGGER=1 php artisan …</Code></li>
              </ul>
            </details>
          </>
        )}
      </div>
    </div>
  );
}
