import i18n, { detectLanguage, languages } from "@/i18n";
import de from "./de.json";
import es from "./es.json";
import fr from "./fr.json";
import itIT from "./it.json";
import nl from "./nl.json";
import pl from "./pl.json";
import pt from "./pt.json";
import ru from "./ru.json";
import uk from "./uk.json";

const translations: Record<string, Record<string, string>> = { de, fr, es, it: itIT, nl, pl, pt, ru, uk };
const plural = /_(one|few|many|other)$/;

describe("i18n", () => {
  afterEach(() => void i18n.changeLanguage("en"));

  it("uses English keys as fallback and German translations when selected", async () => {
    expect(i18n.t("Save")).toBe("Save");
    expect(i18n.t("{{count}} lines", { count: 1 })).toBe("1 line");
    await i18n.changeLanguage("de");
    expect(i18n.t("Save")).toBe("Speichern");
    expect(i18n.t("{{count}} lines", { count: 3 })).toBe("3 Zeilen");
    expect(i18n.t("Open {{url}}", { url: "http://x" })).toBe("http://x öffnen");
  });

  it("keeps every translation's placeholders in sync with the English key", () => {
    const placeholder = /\{\{(\w+)\}\}/g;
    for (const [lang, entries] of Object.entries(translations)) {
      for (const [key, value] of Object.entries(entries)) {
        const base = key.replace(plural, "");
        const want = [...base.matchAll(placeholder)].map((m) => m[1]).sort();
        const got = [...value.matchAll(placeholder)].map((m) => m[1]).sort();
        expect(got, `${lang}: ${key}`).toEqual(want);
      }
    }
  });

  it("translates every German key in every other language", () => {
    const keys = new Set(Object.keys(de).map((k) => k.replace(plural, "")));
    for (const [lang, entries] of Object.entries(translations)) {
      const have = new Set(Object.keys(entries).map((k) => k.replace(plural, "")));
      const missing = [...keys].filter((k) => !have.has(k));
      expect(missing, lang).toEqual([]);
    }
  });

  it("uses the four Slavic plural forms", async () => {
    await i18n.changeLanguage("ru");
    expect(i18n.t("{{count}} lines", { count: 1 })).toBe("1 строка");
    expect(i18n.t("{{count}} lines", { count: 3 })).toBe("3 строки");
    expect(i18n.t("{{count}} lines", { count: 5 })).toBe("5 строк");
    await i18n.changeLanguage("pl");
    expect(i18n.t("{{count}} files", { count: 2 })).toBe("2 pliki");
    expect(i18n.t("{{count}} files", { count: 12 })).toBe("12 plików");
    await i18n.changeLanguage("uk");
    expect(i18n.t("{{count}} errors", { count: 21 })).toBe("21 помилка");
  });

  it("detects the browser language and falls back to English", () => {
    expect(Object.keys(languages)).toContain("de");
    expect(Object.keys(languages)).toContain("uk");
    expect(Object.keys(languages)).toContain(detectLanguage());
  });
});
