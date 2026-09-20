import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import en from "./en.json";

/**
 * Available UI languages. To add one: create src/i18n/<code>.json (English text → translation,
 * see de.json) and add it to `languages` and `loaders`. Languages with more than two plural
 * forms (ru, uk, pl) carry _one/_few/_many/_other keys for every count string.
 */
export const languages: Record<string, string> = {
  en: "English",
  de: "Deutsch",
  fr: "Français",
  es: "Español",
  it: "Italiano",
  nl: "Nederlands",
  pl: "Polski",
  pt: "Português",
  ru: "Русский",
  uk: "Українська",
};
export type Language = string;

type Dictionary = Record<string, string>;

// Only English ships in the main bundle (it carries just the plural forms; every other English
// text is the key itself). Each other language is its own chunk, fetched when first selected.
const loaders: Record<string, () => Promise<{ default: Dictionary }>> = {
  de: () => import("./de.json"),
  fr: () => import("./fr.json"),
  es: () => import("./es.json"),
  it: () => import("./it.json"),
  nl: () => import("./nl.json"),
  pl: () => import("./pl.json"),
  pt: () => import("./pt.json"),
  ru: () => import("./ru.json"),
  uk: () => import("./uk.json"),
};

const lazyBackend = {
  type: "backend" as const,
  init() {},
  read(lng: string, _ns: string, done: (err: unknown, data?: Dictionary) => void) {
    const load = loaders[lng];
    if (!load) {
      done(null, {});
      return;
    }
    load().then(
      (m) => done(null, m.default),
      (err: unknown) => done(err),
    );
  },
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

export function setLanguage(lang: Language): Promise<void> {
  try {
    localStorage.setItem(STORAGE_KEY, lang);
  } catch {
    /* storage unavailable */
  }
  document.documentElement.lang = lang;
  // Resolves once the dictionary is loaded; react-i18next re-renders on its own.
  return i18n.changeLanguage(lang).then(() => undefined);
}

/**
 * English is the source language: every t() call carries its English text as default,
 * so only the other dictionaries are maintained separately.
 */
export const ready: Promise<void> = i18n
  .use(lazyBackend)
  .use(initReactI18next)
  .init({
    resources: { en: { translation: en } },
    partialBundledLanguages: true,
    lng: detectLanguage(),
    fallbackLng: "en",
    interpolation: { escapeValue: false },
    returnEmptyString: false,
    keySeparator: false,
    nsSeparator: false,
    react: { useSuspense: false },
  })
  .then(() => undefined);

export default i18n;
