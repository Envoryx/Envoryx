import type { PHPConfig, PHPExtension } from "@/api/types";
import { Checkbox, Field, Input, Select } from "@/components/ui";

const sizeOptions = ["64M", "128M", "256M", "512M", "1G", "2G", "-1"];

export function PhpConfigForm({
  value,
  onChange,
  extensions,
}: {
  value: PHPConfig;
  onChange: (next: PHPConfig) => void;
  extensions: PHPExtension[];
}) {
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
                {o === "-1" ? "Unlimited (-1)" : o}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="max_execution_time" htmlFor="php-exec" hint="Seconds, 0 = unlimited">
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
          <Checkbox label="display_errors" description="Show errors in the browser (development)" checked={value.displayErrors} onChange={(e) => set("displayErrors", e.target.checked)} />
        </div>
      </div>

      <div>
        <p className="mb-2 text-sm font-medium text-fg">Extensions</p>
        <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
          {extensions.map((ext) => {
            const checked = ext.builtIn || value.extensions.includes(ext.name);
            const disabled = ext.builtIn || !ext.available;
            return (
              <label
                key={ext.name}
                className={`flex items-start gap-2.5 rounded-md border border-default px-3 py-2 ${disabled ? "opacity-60" : "cursor-pointer hover:bg-muted"}`}
                title={!ext.available ? "Available once Staqio ships its own PHP images (Phase 3)" : ext.builtIn ? "Built into the image" : undefined}
              >
                <input type="checkbox" className="mt-0.5 size-4 accent-accent-600" checked={checked} disabled={disabled} onChange={(e) => toggleExt(ext.name, e.target.checked)} />
                <span>
                  <span className="block font-mono text-xs text-fg">{ext.name}</span>
                  <span className="block text-[11px] text-subtle">
                    {ext.description}
                    {ext.builtIn ? " · built-in" : !ext.available ? " · coming soon" : ""}
                  </span>
                </span>
              </label>
            );
          })}
        </div>
      </div>
    </div>
  );
}
