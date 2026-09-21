import i18n from "@/i18n";
import { auditActionLabel, auditActor, auditDetails } from "./audit";
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
});
