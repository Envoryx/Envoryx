import i18n from "@/i18n";
import { ApiError } from "@/api/client";
import { errorText, localClock, translateMessage, translateSentence } from "./errors";

describe("translateMessage", () => {
  beforeAll(() => i18n.changeLanguage("de"));
  afterAll(() => i18n.changeLanguage("en"));
  const t = i18n.t.bind(i18n);

  it("translates fixed backend messages", () => {
    expect(translateMessage("the Docker engine is not reachable", t)).toBe("Die Docker-Engine ist nicht erreichbar");
  });

  it("translates each segment of a wrapped Go error", () => {
    expect(translateMessage("invalid input: project name must be 2-64 characters", t)).toBe("Ungültige Eingabe: Der Projektname muss 2-64 Zeichen lang sein");
    expect(translateMessage("hostname shop.test is already used: conflict", t)).toBe("Der Hostname shop.test wird bereits verwendet: Konflikt");
    expect(translateMessage("create network: network envoryx-shop: the name is taken by another Envoryx instance on this Docker host", t)).toBe(
      "create network: Netzwerk envoryx-shop: der Name gehört einer anderen Envoryx-Instanz auf diesem Docker-Host",
    );
  });

  it("carries values over from templated messages", () => {
    expect(translateMessage('invalid input: unknown template "laravel"', t)).toBe("Ungültige Eingabe: Unbekannte Vorlage „laravel“");
    expect(translateMessage("conflict: no free port in range 20000-20100", t)).toBe("Konflikt: Kein freier Port im Bereich 20000-20100");
    expect(translateMessage("Request failed (503)", t)).toBe("Anfrage fehlgeschlagen (503)");
  });

  it("keeps the context in front of a templated message", () => {
    expect(translateMessage("not included in the plan: PHP 8.6 is a preview version; this instance's plan includes released versions only", t)).toBe(
      "Nicht im Tarif enthalten: PHP 8.6 ist eine Vorabversion; der Tarif dieser Instanz enthält nur veröffentlichte Versionen",
    );
    expect(translateMessage("not included in the plan: public shares are switched off for this instance", t)).toBe("Nicht im Tarif enthalten: öffentliche Freigaben sind für diese Instanz abgeschaltet");
  });

  it("leaves out the bare kind of error next to a part that stays English", () => {
    expect(translateMessage("conflict: the project has 2 volumes but the renamed plan 3", t)).toBe("the project has 2 volumes but the renamed plan 3");
    expect(translateMessage("something unknown: not found", t)).toBe("something unknown");
    expect(translateMessage('the directory "shop" belongs to project "Shop" already: conflict', t)).toBe(t('the directory "{{path}}" belongs to project "{{project}}" already', { path: "shop", project: "Shop" }) + ": Konflikt");
    expect(translateMessage('conflict: a project named "Shop" already exists', t)).toBe("Konflikt: " + t('a project named "{{name}}" already exists', { name: "Shop" }));
  });

  it("translates the refusals of a request without access", () => {
    expect(translateMessage("no access to this project", t)).toBe("Kein Zugriff auf dieses Projekt");
    expect(translateMessage("your role does not allow this (it needs operate access)", t)).toBe("Deine Rolle erlaubt das nicht (es braucht operate-Zugriff)");
  });

  it("translates the live details of the diagnostics", () => {
    expect(translateMessage("Docker 29.9.0 (API 1.56) on linux, 4 containers running", t)).toBe("Docker 29.9.0 (API 1.56) auf linux, 4 Container laufen");
    expect(translateMessage("*.dev.lan → 192.168.1.5, answered by this Envoryx's proxy (DNS: 127.0.0.11)", t)).toBe("*.dev.lan → 192.168.1.5, beantwortet vom Proxy dieses Envoryx (DNS: 127.0.0.11)");
    expect(translateMessage("0.40.0 is the newest release", t)).toBe("0.40.0 ist das neueste Release");
    expect(translateMessage("ntfy: last delivery failed - HTTP 500", t)).toBe("ntfy: letzte Zustellung fehlgeschlagen - HTTP 500");
  });

  it("names the token scope a role allows", () => {
    expect(translateMessage("your role allows at most read tokens", t)).toBe("Deine Rolle erlaubt höchstens Tokens mit dem Scope read");
    expect(translateMessage("your role allows no API tokens", t)).toBe("Deine Rolle erlaubt keine API-Tokens");
  });

  it("translates the reason inside a health check warning", () => {
    expect(translateMessage("the health check /health has failed since 14:03: HTTP 500 instead of 200", t)).toBe("Der Health Check /health schlägt seit 14:03 fehl: HTTP 500 statt 200");
    expect(translateMessage("the health check /up has failed since 09:15: no answer within 5 s", t)).toBe("Der Health Check /up schlägt seit 09:15 fehl: Keine Antwort innerhalb von 5 s");
  });

  it("takes the plural form of a count in a backend message", () => {
    expect(translateMessage("not included in the plan: this instance's plan runs at most 1 project at the same time; stop another project first", t)).toBe(
      "Nicht im Tarif enthalten: der Tarif dieser Instanz lässt höchstens 1 Projekt gleichzeitig laufen; stoppe zuerst ein anderes Projekt",
    );
    expect(translateMessage("this instance's plan runs at most 3 projects at the same time; stop another project first", t)).toBe("der Tarif dieser Instanz lässt höchstens 3 Projekte gleichzeitig laufen; stoppe zuerst ein anderes Projekt");
    expect(translateMessage("1 project consistent with Docker (checked 10:00:00)", t)).toBe("1 Projekt stimmt mit Docker überein (geprüft 10:00:00)");
  });

  it("writes the server's sizes with the language's decimal separator", () => {
    expect(translateMessage("/config: 533.8 GiB free of 3.6 TiB", t)).toBe("/config: 533,8 GiB von 3,6 TiB frei");
  });

  it("shows a warning's UTC time in the browser's time", () => {
    const clock = (d: Date) => d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
    const now = new Date(Date.UTC(2026, 9, 26, 18, 30));
    expect(localClock("16:02 UTC", now)).toBe(clock(new Date(Date.UTC(2026, 9, 26, 16, 2))));
    // Later than now on the clock: that was yesterday.
    expect(localClock("23:10 UTC", now)).toBe(clock(new Date(Date.UTC(2026, 9, 25, 23, 10))));
    expect(localClock("16:02 CET", now)).toBe("16:02 CET");
    const msg = translateMessage("php ran out of memory at 00:00 UTC (limit 256 MiB); a process was killed", t);
    expect(msg).not.toContain("UTC");
    expect(msg).toMatch(/^php hatte um .+ keinen Speicher mehr/);
  });

  it("translates the status warnings of a project", () => {
    expect(translateMessage("the project should be running but its containers are missing", t)).toBe("Das Projekt sollte laufen, aber seine Container fehlen");
    expect(translateMessage("a newer php image was pulled; restart to apply", t)).toBe("Ein neueres php-Image wurde geladen; zum Übernehmen neu starten");
    expect(translateMessage('container for removed service "redis" still exists', t)).toBe("Ein Container für den entfernten Dienst „redis“ existiert noch");
    expect(translateMessage("the database container runs with an older setup; restart the project to update it", t)).toBe("Der database-Container läuft noch mit einer älteren Einrichtung; starte das Projekt neu, um ihn zu aktualisieren");
    expect(
      translateMessage(
        'the virtual environment was built for Python 3.13 but the container runs 3.14 - its packages are invisible to the new interpreter, so the application server and the workers wait; run "uv sync" from Actions',
        t,
      ),
    ).toBe("Die virtuelle Umgebung wurde für Python 3.13 gebaut, der Container läuft aber mit 3.14 - der neue Interpreter sieht ihre Pakete nicht, daher warten Anwendungsserver und Worker; führe \"uv sync\" unter Aktionen aus");
    expect(translateMessage("Docker engine unavailable: Cannot connect to the Docker daemon", t)).toBe("Docker-Engine nicht erreichbar: Cannot connect to the Docker daemon");
  });

  it("translates why a .NET action or test suite can't run", () => {
    expect(translateMessage("several projects hold EF Core migrations (src/A/A.csproj, src/B/B.csproj) - run dotnet ef with --project in the terminal", t)).toBe(
      "mehrere Projekte enthalten EF-Core-Migrationen (src/A/A.csproj, src/B/B.csproj) - führe dotnet ef im Terminal mit --project aus",
    );
    expect(translateMessage("vendor/bin/pest not found - run composer install", t)).toBe("vendor/bin/pest nicht gefunden - führe composer install aus");
  });

  it("translates a version the host kernel cannot start", () => {
    expect(translateMessage("cannot start MongoDB 8.0 on Linux kernel 6.19 and newer (this host runs 6.19.0-31-generic); switch to MongoDB 8.2", t)).toBe(
      "MongoDB 8.0 startet nicht auf Linux-Kernel 6.19 und neuer (dieser Host hat 6.19.0-31-generic); wechsle zu MongoDB 8.2",
    );
    expect(translateMessage("invalid input: cannot start MongoDB 7.0 on Linux kernel 6.19 and newer (this host runs 7.0.0-31-generic)", t)).toBe(
      "Ungültige Eingabe: MongoDB 7.0 startet nicht auf Linux-Kernel 6.19 und neuer (dieser Host hat 7.0.0-31-generic)",
    );
  });

  it("names what a PHP version's image lacks", () => {
    expect(translateMessage("invalid input: PHP 8.6 does not ship Xdebug yet; switch it off or choose another PHP version", t)).toBe("Ungültige Eingabe: PHP 8.6 enthält Xdebug noch nicht; schalte es aus oder wähle eine andere PHP-Version");
    expect(translateMessage("invalid input: PHP 8.6 does not ship imagick, redis yet; switch them off or choose another PHP version", t)).toBe("Ungültige Eingabe: PHP 8.6 enthält imagick, redis noch nicht; schalte sie aus oder wähle eine andere PHP-Version");
  });

  it("translates the way forward for a database version the kernel cannot start", () => {
    expect(translateMessage("cannot start MongoDB 8.0 on Linux kernel 6.19 and newer (this host runs 7.0.0-31-generic); upgrade the database to MongoDB 8.2, which takes over its data", t)).toBe(
      "MongoDB 8.0 startet nicht auf Linux-Kernel 6.19 und neuer (dieser Host hat 7.0.0-31-generic); aktualisiere die Datenbank auf MongoDB 8.2, das ihre Daten übernimmt",
    );
    expect(
      translateMessage(
        "invalid input: cannot start MongoDB 7.0 on Linux kernel 6.19 and newer (this host runs 7.0.0-31-generic), and MongoDB 8.2 cannot take over its data; export it on a host with an older kernel, or remove and re-add the database (its data is lost)",
        t,
      ),
    ).toBe(
      "Ungültige Eingabe: MongoDB 7.0 startet nicht auf Linux-Kernel 6.19 und neuer (dieser Host hat 7.0.0-31-generic), und MongoDB 8.2 kann ihre Daten nicht übernehmen; exportiere sie auf einem Host mit älterem Kernel oder entferne die Datenbank und füge sie neu hinzu (ihre Daten gehen verloren)",
    );
    expect(translateMessage("restore database the primary database: invalid input: the backup holds a copy of mongodb 7 data, which mongodb 8.2 cannot open", t)).toContain(
      "Das Backup enthält eine Kopie von Daten aus mongodb 7, die mongodb 8.2 nicht öffnen kann",
    );
  });

  it("names a project file that needs a newer runtime", () => {
    expect(translateMessage("pom.xml needs Java 25 but the project runs Java 21, so the build or the start fails; select a newer version in the settings or lower the requirement in the file", t)).toBe(
      "pom.xml braucht Java 25, das Projekt läuft aber mit Java 21, deshalb schlägt der Build oder der Start fehl; wähle in den Einstellungen eine neuere Version oder senke die Anforderung in der Datei",
    );
    expect(translateMessage("global.json needs .NET SDK 10.0.100 but the project runs .NET 8, so the build or the start fails; select a newer version in the settings or lower the requirement in the file", t)).toContain(
      "global.json braucht .NET SDK 10.0.100, das Projekt läuft aber mit .NET 8",
    );
  });

  it("leaves unknown text alone", () => {
    expect(translateMessage("something: quite unexpected", t)).toBe("something: quite unexpected");
    expect(translateMessage("", t)).toBe("");
  });

  it("translates validation errors of forms, branch environments and cron jobs", () => {
    expect(translateMessage("invalid input: server port must be between 1024 and 65535", t)).toBe("Ungültige Eingabe: Der Server-Port muss zwischen 1024 und 65535 liegen");
    expect(translateMessage("conflict: the branch feature-x already has the environment shop-feature-x", t)).toBe("Konflikt: Der Branch feature-x hat bereits die Umgebung shop-feature-x");
    // The cron field is a fixed word of the server's and gets translated with the sentence.
    expect(translateMessage('invalid cron schedule: "25" is not a valid hour (0-23)', t)).toBe("Ungültiger Cron-Zeitplan: „25“ ist kein gültiger Wert für Stunde (0-23)");
  });

  it("translates the fixed phrases a template carries, but not names", () => {
    expect(translateMessage("dlv is missing: no debugging", t)).toBe("dlv fehlt; das betrifft: Debugging");
    expect(translateMessage("invalid input: API token is required for Cloudflare", t)).toBe("Ungültige Eingabe: API-Token ist für Cloudflare erforderlich");
    expect(translateMessage('invalid input: healthcheck.interval "5x" is not a duration in whole seconds like 30s or 2m', t)).toBe(
      "Ungültige Eingabe: healthcheck.interval „5x“ ist keine Dauer in ganzen Sekunden wie 30s oder 2m",
    );
  });

  it("translates a reason inside a message and prefers the specific template", () => {
    expect(
      translateMessage(
        "invalid input: the build context, the whole project directory, is too large (more than 10000 files or 500 MB): put the Dockerfile in a directory of its own, like .envoryx/, with just the files the image needs",
        t,
      ),
    ).toBe("Ungültige Eingabe: Der Build-Kontext, das ganze Projektverzeichnis, ist zu groß (mehr als 10000 Dateien oder 500 MB): Lege das Dockerfile in ein eigenes Verzeichnis wie .envoryx/, nur mit den Dateien, die das Image braucht");
    expect(translateMessage("invalid input: the image ghcr.io/acme/php:8.4 could not be pulled; if the registry is private, add a login under Settings, Private registries (denied)", t)).toBe(
      "Ungültige Eingabe: Das Image ghcr.io/acme/php:8.4 ließ sich nicht laden; ist die Registry privat, füge unter Einstellungen, Private Registries ein Login hinzu (denied)",
    );
    expect(translateMessage("cloudflare: token rejected (Invalid API Token); it needs Zone:Read and Zone:DNS:Edit for the zone", t)).toBe(
      "cloudflare: Token abgelehnt (Invalid API Token); es braucht Zone:Read und Zone:DNS:Edit für die Zone",
    );
  });

  it("returns the raw English text when English is active", async () => {
    await i18n.changeLanguage("en");
    expect(translateMessage('invalid input: unknown template "laravel"', t)).toBe('invalid input: unknown template "laravel"');
    // The prefix stays also in front of a message with no template, as in the other languages.
    expect(translateMessage("invalid input: worker name must be 1-32 lowercase letters", t)).toBe("invalid input: worker name must be 1-32 lowercase letters");
    expect(errorText(new ApiError(403, "wrong_password", "the current password is wrong"), t)).toBe("the current password is wrong");
    await i18n.changeLanguage("de");
  });

  it("starts a translated message shown on its own with a capital letter", () => {
    expect(errorText(new ApiError(403, "wrong_password", "the current password is wrong"), t)).toBe("Das aktuelle Passwort ist falsch");
    expect(translateSentence("the current password is wrong", t)).toBe("Das aktuelle Passwort ist falsch");
    // A value at the start keeps its case.
    expect(translateSentence("composer.json not found in project", t)).toBe("composer.json wurde im Projekt nicht gefunden");
    // Inside a sentence it stays as it is.
    expect(translateMessage("the current password is wrong", t)).toBe("das aktuelle Passwort ist falsch");
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
