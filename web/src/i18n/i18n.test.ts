import i18n, { detectLanguage, languages } from "@/i18n";
import de from "./de.json";

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

  it("keeps every German entry's placeholders in sync with the English key", () => {
    const placeholder = /\{\{(\w+)\}\}/g;
    for (const [key, value] of Object.entries(de as Record<string, string>)) {
      const base = key.replace(/_(one|other)$/, "");
      const want = [...base.matchAll(placeholder)].map((m) => m[1]).sort();
      const got = [...value.matchAll(placeholder)].map((m) => m[1]).sort();
      expect(got, key).toEqual(want);
    }
  });

  it("detects the browser language and falls back to English", () => {
    expect(Object.keys(languages)).toContain("de");
    expect(["en", "de"]).toContain(detectLanguage());
  });
});
