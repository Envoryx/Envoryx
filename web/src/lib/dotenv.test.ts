import { looksSecret, parseDotenv, serializeDotenv } from "./dotenv";

describe("parseDotenv", () => {
  it("reads the forms Laravel, Symfony and docker compose write", () => {
    const { entries, problems } = parseDotenv(
      [
        "# Laravel",
        "APP_NAME=Shop",
        "export APP_ENV=local",
        "APP_URL = http://localhost # the dev server",
        "SINGLE='literal ${APP_NAME} # not a comment'",
        'DOUBLE="Hello \\"World\\"\\n${APP_NAME}"',
        "VITE_APP_NAME=\"${APP_NAME}\"",
        "UNKNOWN=${NOPE}",
        "EMPTY=",
        'MULTI="line one',
        'line two"',
        "not a variable",
        "APP_NAME=Shop2",
      ].join("\n"),
    );
    const map = Object.fromEntries(entries.map((e) => [e.key, e.value]));
    expect(map).toEqual({
      APP_NAME: "Shop2",
      APP_ENV: "local",
      APP_URL: "http://localhost",
      SINGLE: "literal ${APP_NAME} # not a comment",
      DOUBLE: 'Hello "World"\nShop',
      VITE_APP_NAME: "Shop",
      UNKNOWN: "${NOPE}",
      EMPTY: "",
      MULTI: "line one\nline two",
    });
    expect(problems).toEqual([{ line: 12, text: "not a variable" }]);
    // The last definition wins and keeps its place.
    expect(entries.at(-1)).toMatchObject({ key: "APP_NAME", line: 13 });
  });
});

describe("serializeDotenv", () => {
  it("quotes what needs it and reads back to the same values", () => {
    const vars = [
      { key: "PLAIN", value: "abc-1.2" },
      { key: "URL", value: "mysql://u:p@db:3306/x" },
      { key: "SPACES", value: "two words" },
      { key: "TRICKY", value: 'a "quote", a $dollar and a \\ backslash' },
      { key: "EMPTY", value: "" },
    ];
    const text = serializeDotenv(vars, "Exported by Envoryx");
    expect(text).toContain("# Exported by Envoryx\n");
    expect(text).toContain("PLAIN=abc-1.2\n");
    expect(text).toContain('SPACES="two words"\n');
    expect(parseDotenv(text).entries.map(({ key, value }) => ({ key, value }))).toEqual(vars);
  });
});

describe("looksSecret", () => {
  it("goes by the name and by credentials in URLs", () => {
    for (const k of ["DB_PASSWORD", "APP_KEY", "STRIPE_SECRET", "GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "MAIL_PASSWORD", "JWT_PASSPHRASE_PRIVATE", "AUTH_SALT"]) {
      expect(looksSecret(k, "x"), k).toBe(true);
    }
    for (const k of ["APP_NAME", "DB_HOST", "MAIL_PORT", "KEYBOARD_LAYOUT"]) {
      expect(looksSecret(k, "x"), k).toBe(false);
    }
    expect(looksSecret("DATABASE_URL", "postgresql://app:s3cret@db:5432/app")).toBe(true);
    expect(looksSecret("REDIS_URL", "redis://redis:6379")).toBe(false);
  });
});
