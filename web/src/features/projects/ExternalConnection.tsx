import { PlugZap } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { api } from "@/api/client";
import type { ExternalDatabase, ExternalRedis } from "@/api/types";
import { Alert, Button, Field, Input } from "@/components/ui";
import { errorText } from "@/lib/errors";

/** The flavours an external database may be (runtime.ExternalVariants). */
export const externalDatabaseTypes = ["mariadb", "mysql", "postgresql"];

const defaultPorts: Record<string, number> = { mariadb: 3306, mysql: 3306, postgresql: 5432, redis: 6379 };

export const emptyExternalDatabase: ExternalDatabase = { host: "", port: 0, username: "", password: "", database: "" };
export const emptyExternalRedis: ExternalRedis = { host: "", port: 0, password: "" };

/** Whether the fields are filled enough to try or store the connection. */
export function externalDatabaseComplete(c: ExternalDatabase, passwordOptional = false): boolean {
  return !!c.host.trim() && !!c.username.trim() && !!c.database.trim() && (passwordOptional || c.password !== "");
}

/** "Test connection": tries it on the server the way the project will, with the outcome underneath. */
function TestButton({ disabled, run }: { disabled: boolean; run: () => Promise<unknown> }) {
  const { t } = useTranslation();
  const [state, setState] = useState<{ busy: boolean; ok?: boolean; error?: string }>({ busy: false });
  return (
    <div className="space-y-2">
      <Button
        size="sm"
        icon={<PlugZap className="size-3.5" />}
        disabled={disabled}
        loading={state.busy}
        onClick={async () => {
          setState({ busy: true });
          try {
            await run();
            setState({ busy: false, ok: true });
          } catch (err) {
            setState({ busy: false, ok: false, error: errorText(err, t, t("The test could not be started")) });
          }
        }}
      >
        {t("Test connection")}
      </Button>
      {state.ok === true && <Alert tone="green">{t("Connected.")}</Alert>}
      {state.ok === false && <Alert tone="red">{state.error}</Alert>}
    </div>
  );
}

const hostHint = (t: (k: string) => string) => t("A server on the Docker host itself (e.g. the MariaDB of your Unraid server) is reached as host.docker.internal; localhost would be the container.");

/**
 * Host, port, user, password and database of a database server Envoryx does not run, and a
 * button that tries them. passwordOptional: editing a stored connection, where an empty
 * password keeps the stored one.
 */
export function ExternalDatabaseFields({ id, type, version, value, onChange, passwordOptional = false }: { id: string; type: string; version: string; value: ExternalDatabase; onChange: (v: ExternalDatabase) => void; passwordOptional?: boolean }) {
  const { t } = useTranslation();
  const set = (patch: Partial<ExternalDatabase>) => onChange({ ...value, ...patch });
  return (
    <div className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-[1fr_8rem]">
        <Field label={t("Host")} htmlFor={`${id}-host`} hint={hostHint(t)}>
          <Input id={`${id}-host`} value={value.host} onChange={(e) => set({ host: e.target.value.trim() })} placeholder="db.example.com" spellCheck={false} autoComplete="off" />
        </Field>
        <Field label={t("Port")} htmlFor={`${id}-port`}>
          <Input id={`${id}-port`} type="number" min={1} max={65535} value={value.port || ""} onChange={(e) => set({ port: Number(e.target.value) || 0 })} placeholder={String(defaultPorts[type] ?? "")} />
        </Field>
      </div>
      <div className="grid gap-4 sm:grid-cols-3">
        <Field label={t("Database")} htmlFor={`${id}-database`}>
          <Input id={`${id}-database`} value={value.database} onChange={(e) => set({ database: e.target.value.trim() })} spellCheck={false} autoComplete="off" />
        </Field>
        <Field label={t("Username")} htmlFor={`${id}-username`}>
          <Input id={`${id}-username`} value={value.username} onChange={(e) => set({ username: e.target.value.trim() })} spellCheck={false} autoComplete="off" />
        </Field>
        <Field label={t("Password")} htmlFor={`${id}-password`} hint={passwordOptional ? t("Leave empty to keep the stored password.") : undefined}>
          <Input id={`${id}-password`} type="password" value={value.password} onChange={(e) => set({ password: e.target.value })} autoComplete="new-password" />
        </Field>
      </div>
      <TestButton
        disabled={!externalDatabaseComplete(value, false)}
        run={() => api.external.test({ kind: "database", type, version, ...value })}
      />
    </div>
  );
}

/** Host, port and password of a Redis Envoryx does not run, and a button that tries them. */
export function ExternalRedisFields({ id, value, onChange, passwordOptional = false }: { id: string; value: ExternalRedis; onChange: (v: ExternalRedis) => void; passwordOptional?: boolean }) {
  const { t } = useTranslation();
  const set = (patch: Partial<ExternalRedis>) => onChange({ ...value, ...patch });
  return (
    <div className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-[1fr_8rem]">
        <Field label={t("Host")} htmlFor={`${id}-host`} hint={hostHint(t)}>
          <Input id={`${id}-host`} value={value.host} onChange={(e) => set({ host: e.target.value.trim() })} placeholder="cache.example.com" spellCheck={false} autoComplete="off" />
        </Field>
        <Field label={t("Port")} htmlFor={`${id}-port`}>
          <Input id={`${id}-port`} type="number" min={1} max={65535} value={value.port || ""} onChange={(e) => set({ port: Number(e.target.value) || 0 })} placeholder="6379" />
        </Field>
      </div>
      <Field label={t("Password (optional)")} htmlFor={`${id}-password`} hint={passwordOptional ? t("Leave empty to keep the stored password.") : undefined}>
        <Input id={`${id}-password`} type="password" value={value.password} onChange={(e) => set({ password: e.target.value })} autoComplete="new-password" />
      </Field>
      <TestButton disabled={!value.host.trim() || (passwordOptional && value.password === "")} run={() => api.external.test({ kind: "redis", ...value })} />
    </div>
  );
}
