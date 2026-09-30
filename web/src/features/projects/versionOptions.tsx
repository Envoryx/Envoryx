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

/** Why versions of the list cannot run on this host, as a hint under the select ("" for none). */
export function unavailableHint(versions: RuntimeVersion[], t: TFunction, anyHost = false): string {
  if (anyHost) return "";
  const reasons = [...new Set(versions.filter((v) => v.unavailable && v.unavailableReason).map((v) => translateMessage(v.unavailableReason, t)))];
  return reasons.length ? reasons.join(". ") + "." : "";
}
