/** The sections of the settings page; `?tab=` names one. */
export type SettingsTab =
  | "profile"
  | "tokens"
  | "sshkeys"
  | "diagnostics"
  | "general"
  | "domains"
  | "users"
  | "ssh"
  | "deploykey"
  | "secretkey"
  | "backups"
  | "notifications"
  | "retention"
  | "audit"
  | "addons"
  | "dbtool"
  | "packagecache"
  | "registries";

/** Where a link into the settings leads. */
export function settingsHref(tab: SettingsTab) {
  return `/settings?tab=${tab}`;
}
