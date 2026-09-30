import type { TFunction } from "i18next";
import type { RuntimeVersion } from "@/api/types";
import { translateMessage } from "@/lib/errors";

/**
 * The options of a version select. A version the Docker host cannot run (the server marks
 * it unavailable, e.g. MongoDB 8.0 on Linux 6.19) is disabled, except `keep`: the version a
 * service already has stays selectable so its form still saves. `anyHost` lifts the lock
 * where the version only picks client tools (an external server).
 */
export function versionOptions(versions: RuntimeVersion[], t: TFunction, opts: { keep?: string; anyHost?: boolean } = {}) {
  return versions.map((v) => {
    const unavailable = !opts.anyHost && !!v.unavailable;
    return (
      <option key={v.version} value={v.version} disabled={unavailable && v.version !== opts.keep}>
        {v.label}
        {v.eol ? t(" (end of life)") : v.preview ? t(" (preview)") : ""}
        {unavailable ? t(" (not on this host)") : ""}
      </option>
    );
  });
}

/**
 * Why versions of the list cannot run on this host, as a hint under the select ("" for none).
 * `keep` (the version a database already has) is left out: its advice depends on the data it
 * holds and comes with the project's status warning.
 */
export function unavailableHint(versions: RuntimeVersion[], t: TFunction, anyHost = false, keep?: string): string {
  if (anyHost) return "";
  const reasons = [...new Set(versions.filter((v) => v.unavailable && v.unavailableReason && v.version !== keep).map((v) => translateMessage(v.unavailableReason, t)))];
  return reasons.length ? reasons.join(". ") + "." : "";
}

/**
 * What a MongoDB database can be upgraded to on its data: the versions that name its version in
 * `upgradesFrom`. Anything else needs an export, remove and re-add.
 */
export function mongoUpgradeHint(versions: RuntimeVersion[], current: string, t: TFunction): string {
  const targets = versions.filter((v) => v.upgradesFrom?.includes(current)).map((v) => v.label);
  return targets.length
    ? t("{{versions}} takes over this data in place, and a database backup is taken automatically first. Other versions need an export, remove and re-add.", { versions: targets.join(", ") })
    : t("MongoDB cannot move this data to another version in place: export it, remove the database and add it again.");
}
