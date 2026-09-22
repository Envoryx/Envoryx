import type { TFunction } from "i18next";
import type { Serves } from "@/api/types";

/** Display names of the project web servers (catalogue keys). */
export const webServerNames: Record<string, string> = { caddy: "Caddy", apache: "Apache", nginx: "Nginx" };

/**
 * One-line guidance shown next to the web server selector. What the server does depends on
 * what the project serves: FastCGI to PHP, standing by behind a Node dev server, or static files.
 * Callers pass `project.serves ?? servesOf(project)`; the default only covers the PHP-era call shape.
 */
export function webServerHint(t: TFunction, key: string, serves: Serves = "php"): string {
  if (serves === "node") {
    return t("The dev server answers on the project URL; the web server only serves the document root once the dev server is turned off (e.g. for npm run build output).");
  }
  if (serves === "python") {
    return t("The application server answers on the project URL; the web server only serves the document root once the server is turned off (e.g. collected static files).");
  }
  if (serves === "static") {
    switch (key) {
      case "apache":
        return t("Apache honours .htaccess files (mod_rewrite, access rules) and serves the document root statically.");
      case "nginx":
        return t("Nginx serves the document root statically; unknown paths return 404 unless the SPA fallback is on.");
      default:
        return t("Caddy serves the document root statically; unknown paths return 404 unless the SPA fallback is on.");
    }
  }
  switch (key) {
    case "apache":
      return t("Apache honours .htaccess files (mod_rewrite, access rules) – the right choice for WordPress, TYPO3 and projects that ship one for production.");
    case "nginx":
      return t("Nginx routes unknown paths to index.php (front controller). .htaccess files are ignored, matching a typical Nginx production setup.");
    default:
      return t("Caddy needs no configuration and routes unknown paths to index.php (front controller). .htaccess files are ignored.");
  }
}
