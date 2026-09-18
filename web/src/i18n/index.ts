import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import de from "./de.json";
import en from "./en.json";

/**
 * Available UI languages. To add one: create src/i18n/<code>.json (English text → translation,
 * see de.json), import it here and add it to `languages` and `resources`.
 */
export const languages: Record<string, string> = { en: "English", de: "Deutsch" };
export type Language = string;

// English carries only plural forms; every other English text is the key itself.
const resources: Record<string, { translation: Record<string, string> }> = {
  en: { translation: en },
  de: { translation: de },
};

const STORAGE_KEY = "envoryx.lang";

export function detectLanguage(): Language {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored && languages[stored]) return stored;
  } catch {
    /* storage unavailable */
  }
  const nav = (typeof navigator !== "undefined" ? navigator.language : "en").toLowerCase();
  for (const code of Object.keys(languages)) {
    if (nav === code || nav.startsWith(code + "-")) return code;
  }
  return "en";
}

/** Normalises i18next's current language to one of `languages`. */
export function currentLanguage(): Language {
  const l = i18n.language ?? "en";
  return languages[l] ? l : (Object.keys(languages).find((c) => l.startsWith(c + "-")) ?? "en");
}

export function setLanguage(lang: Language): void {
  try {
    localStorage.setItem(STORAGE_KEY, lang);
  } catch {
    /* storage unavailable */
  }
  void i18n.changeLanguage(lang);
  document.documentElement.lang = lang;
}

/**
 * English is the source language: every t() call carries its English text as default,
 * so only the German dictionary is maintained separately.
 */
void i18n.use(initReactI18next).init({
  resources,
  lng: detectLanguage(),
  fallbackLng: "en",
  interpolation: { escapeValue: false },
  returnEmptyString: false,
  keySeparator: false,
  nsSeparator: false,
});

export default i18n;
