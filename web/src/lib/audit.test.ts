import i18n from "@/i18n";
import { auditActionLabel, auditActor, auditChangeLabel, auditDetails } from "./audit";
import type { AuditEntry } from "@/api/types";

const entry = (over: Partial<AuditEntry>): AuditEntry => ({ id: "1", createdAt: "2026-09-21T06:00:00Z", username: "", action: "", targetType: "", targetId: "", details: null, ip: "", ...over });

describe("audit helpers", () => {
  const t = i18n.t.bind(i18n);

  it("labels actions, names Envoryx for automatic entries and summarises details", () => {
    const e = entry({ action: "docker.orphans_removed", targetType: "docker", details: { automatic: true, removed: ["container envoryx-ghost-php", "network envoryx-ghost"] } });
    expect(auditActionLabel(e.action, t)).toBe("Orphaned resources removed");
    expect(auditActor(e, t)).toBe("Envoryx (automatic)");
    expect(auditDetails(e, t)).toBe("container envoryx-ghost-php, network envoryx-ghost");
    expect(auditActor(entry({ username: "admin" }), t)).toBe("admin");
    expect(auditActionLabel("something.new", t)).toBe("something.new");
    expect(auditDetails(entry({ action: "project.started", targetType: "project", targetId: "3f0b4a9e-1a2b", details: { name: "Shop" } }), t)).toBe("Shop");
    expect(auditDetails(entry({ action: "settings.changed", details: { publicHost: "nas", forceHttps: true } }), t)).toBe("publicHost, forceHttps");
  });

  it("labels every account action and names users instead of showing their id", () => {
    for (const a of ["user.invited", "user.joined", "user.updated", "user.deleted"]) expect(auditActionLabel(a, t)).not.toBe(a);
    expect(auditDetails(entry({ action: "user.invited", targetType: "user", targetId: "b673e09e-77", details: { username: "mia", role: "developer" } }), t)).toBe("mia");
    expect(auditDetails(entry({ action: "terminal.opened", targetType: "project", targetId: "7cabed54-11", details: { service: "php" } }), t)).toBe("project 7cabed54");
    expect(auditDetails(entry({ action: "auth.login_failed", targetType: "user", details: { reason: "invalid credentials" } }), t)).toBe("invalid credentials");
    expect(auditDetails(entry({ action: "auth.logout", targetType: "user", details: {} }), t)).toBe("-");
  });

  it("names the changes of a project update readably and in the chosen language", async () => {
    const updated = (changes: Record<string, unknown>) => entry({ action: "project.updated", details: { name: "ui-empty", changes } });
    expect(auditDetails(updated({ domainAdded: "shop.example.com" }), t)).toBe("ui-empty · Domain added");
    expect(auditDetails(updated({ "addon:phpmyadmin": "5.2", "addon:phpmyadminHostPort": 20010 }), t)).toBe("ui-empty · Addon phpmyadmin, Addon phpmyadmin host port");
    expect(auditDetails(updated({ cronJobAdded: "nightly", schedule: "0 3 * * *", command: "php artisan x" }), t)).toBe("ui-empty · Cron job added");
    expect(auditDetails(updated({ logHistory: true, logHistoryDays: 7 }), t)).toBe("ui-empty · Log history");
    expect(auditChangeLabel("redisHostPort", t)).toBe("Redis host port");
    expect(auditChangeLabel("databaseVersion:analytics", t)).toBe("Database version (analytics)");
    expect(auditChangeLabel("nodeDevServer", t)).toBe("Node.js dev server");
    expect(auditChangeLabel("somethingNew", t)).toBe("somethingNew");
    await i18n.changeLanguage("de");
    try {
      expect(auditDetails(updated({ domainAdded: "shop.example.com" }), t)).toBe("ui-empty · Domain hinzugefügt");
      // A failed sign-in's reason is the server's English text: translated, as a sentence.
      expect(auditDetails(entry({ action: "auth.login_failed", details: { reason: "invalid credentials" } }), t)).toBe("Falscher Benutzername oder falsches Passwort");
      expect(auditDetails(entry({ action: "auth.login_failed", details: { reason: "the current password is wrong" } }), t)).toBe("Das aktuelle Passwort ist falsch");
    } finally {
      await i18n.changeLanguage("en");
    }
  });
});
