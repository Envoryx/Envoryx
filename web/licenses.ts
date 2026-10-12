import { existsSync, readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import type { Plugin } from "vite";

/** Files in a package that carry its licence or attribution notices. */
const NOTICE_FILE = /^(licen[cs]e|copying|notice)([._-].*)?$/i;

/**
 * Writes the licence texts of every npm package that ends up in the bundle to fileName,
 * so they ship with the web interface. extra names packages the bundle contains without
 * importing them as modules (Tailwind's preflight in the CSS).
 */
export function thirdPartyLicenses(fileName: string, extra: string[] = []): Plugin {
  const root = process.cwd();
  return {
    name: "envoryx-third-party-licenses",
    apply: "build",
    generateBundle(_, bundle) {
      const dirs = new Set(extra.map((name) => join(root, "node_modules", name)));
      for (const out of Object.values(bundle)) {
        if (out.type !== "chunk") continue;
        for (const id of Object.keys(out.modules)) {
          const dir = packageDir(id);
          if (dir) dirs.add(dir);
        }
      }
      const entries = new Map<string, string>();
      for (const dir of dirs) {
        const pkg = JSON.parse(readFileSync(join(dir, "package.json"), "utf8")) as { name: string; version: string; license?: string; homepage?: string };
        const key = `${pkg.name}@${pkg.version}`;
        if (entries.has(key)) continue;
        const files = readdirSync(dir).filter((f) => NOTICE_FILE.test(f)).sort();
        if (files.length === 0) this.error(`${key} has no licence file; add its licence text by hand`);
        const texts = files.map((f) => readFileSync(join(dir, f), "utf8").trim());
        const head = `${pkg.name} ${pkg.version}${pkg.license ? ` (${pkg.license})` : ""}`;
        entries.set(key, [head, ...(pkg.homepage ? [pkg.homepage] : []), "-".repeat(80), texts.join("\n\n")].join("\n"));
      }
      const body = [...entries.keys()].sort().map((k) => entries.get(k)).join(`\n\n${"=".repeat(80)}\n`);
      this.emitFile({ type: "asset", fileName, source: `${"=".repeat(80)}\n${body}\n` });
    },
  };
}

/** The directory of the npm package a module id belongs to, or "" for the app's own code. */
function packageDir(id: string): string {
  const path = id.replace(/^\0/, "").replace(/\?.*$/, "");
  const at = path.lastIndexOf("/node_modules/");
  if (at < 0) return "";
  const rest = path.slice(at + "/node_modules/".length).split("/");
  const name = rest[0].startsWith("@") ? `${rest[0]}/${rest[1]}` : rest[0];
  const dir = path.slice(0, at) + "/node_modules/" + name;
  return existsSync(join(dir, "package.json")) ? dir : "";
}
