import { describe, expect, it } from "vitest";
import { hostPathOf } from "./hostPath";

const status = (o: Partial<{ overrides: Record<string, string>; detected: Record<string, string>; bareMetal: boolean }>) => ({
  hostPath: { selfContainerId: "", overrides: {}, detected: {}, bareMetal: false, ...o },
});

describe("hostPathOf", () => {
  it("prefers the override, then the detected mount", () => {
    expect(hostPathOf(status({ overrides: { "/projects": "/srv/a" }, detected: { "/projects": "/srv/b" } }), "/projects")).toBe("/srv/a");
    expect(hostPathOf(status({ detected: { "/projects": "/srv/b" } }), "/projects")).toBe("/srv/b");
  });
  it("is the directory itself on bare metal", () => {
    expect(hostPathOf(status({ bareMetal: true }), "/home/me/projects")).toBe("/home/me/projects");
  });
  it("stays unknown in a container without a mount", () => {
    expect(hostPathOf(status({}), "/projects")).toBeUndefined();
    expect(hostPathOf(undefined, "/projects")).toBeUndefined();
  });
});
