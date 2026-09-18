import type { TFunction } from "i18next";

/** Display names of the project web servers (catalogue keys). */
export const webServerNames: Record<string, string> = { caddy: "Caddy", apache: "Apache", nginx: "Nginx" };

/** One-line guidance shown next to the web server selector. */
export function webServerHint(t: TFunction, key: string): string {
  switch (key) {
    case "apache":
      return t("Apache honours .htaccess files (mod_rewrite, access rules) – the right choice for WordPress, TYPO3 and projects that ship one for production.");
    case "nginx":
      return t("Nginx routes unknown paths to index.php (front controller). .htaccess files are ignored, matching a typical Nginx production setup.");
    default:
      return t("Caddy needs no configuration and routes unknown paths to index.php (front controller). .htaccess files are ignored.");
  }
}
