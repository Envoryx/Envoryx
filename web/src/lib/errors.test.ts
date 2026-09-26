import i18n from "@/i18n";
import { ApiError } from "@/api/client";
import { errorText, translateMessage } from "./errors";

describe("translateMessage", () => {
  beforeAll(() => i18n.changeLanguage("de"));
  afterAll(() => i18n.changeLanguage("en"));
  const t = i18n.t.bind(i18n);

  it("translates fixed backend messages", () => {
    expect(translateMessage("the Docker engine is not reachable", t)).toBe("Die Docker-Engine ist nicht erreichbar");
  });

  it("translates each segment of a wrapped Go error", () => {
    expect(translateMessage("invalid input: project name must be 2-64 characters", t)).toBe("Ungültige Eingabe: Der Projektname muss 2–64 Zeichen lang sein");
    expect(translateMessage("hostname shop.test is already used: conflict", t)).toBe("Der Hostname shop.test wird bereits verwendet: Konflikt");
  });

  it("carries values over from templated messages", () => {
    expect(translateMessage('invalid input: unknown template "laravel"', t)).toBe("Ungültige Eingabe: Unbekannte Vorlage „laravel“");
    expect(translateMessage("conflict: no free port in range 20000-20100", t)).toBe("Konflikt: Kein freier Port im Bereich 20000–20100");
    expect(translateMessage("Request failed (503)", t)).toBe("Anfrage fehlgeschlagen (503)");
  });

  it("translates the reason inside a health check warning", () => {
    expect(translateMessage("the health check /health has failed since 14:03: HTTP 500 instead of 200", t)).toBe("Der Health Check /health schlägt seit 14:03 fehl: HTTP 500 statt 200");
    expect(translateMessage("the health check /up has failed since 09:15: no answer within 5 s", t)).toBe("Der Health Check /up schlägt seit 09:15 fehl: Keine Antwort innerhalb von 5 s");
  });

  it("leaves unknown text alone", () => {
    expect(translateMessage("something: quite unexpected", t)).toBe("something: quite unexpected");
    expect(translateMessage("", t)).toBe("");
  });

  it("returns the raw English text when English is active", async () => {
    await i18n.changeLanguage("en");
    expect(translateMessage('invalid input: unknown template "laravel"', t)).toBe('invalid input: unknown template "laravel"');
    await i18n.changeLanguage("de");
  });
});

describe("errorText", () => {
  const t = i18n.t.bind(i18n);
  it("uses the server message for API errors and the fallback otherwise", () => {
    expect(errorText(new ApiError(409, "busy", "project is busy with another operation"), t)).toBe("project is busy with another operation");
    expect(errorText(new TypeError("Failed to fetch"), t, "Saving failed")).toBe("Saving failed");
    expect(errorText(new TypeError("Failed to fetch"), t)).toBe("The server could not be reached.");
  });
});

describe("external connection messages", () => {
  afterEach(() => void i18n.changeLanguage("en"));

  it("translates a failed connection and keeps the server's own words", async () => {
    await i18n.changeLanguage("de");
    const msg = "invalid input: cannot connect to mariadb at host.docker.internal:23306 as shop_app: ERROR 1045 (28000): Access denied for user 'shop_app'@'172.17.0.1' (using password: YES)";
    const out = translateMessage(msg, i18n.t);
    expect(out).toContain("keine Verbindung zu mariadb unter host.docker.internal:23306 als shop_app");
    expect(out).toContain("Access denied for user 'shop_app'");
  });
});
