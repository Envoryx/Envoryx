import { covers, projectAccess } from "./access";

describe("covers", () => {
  it("orders the levels like the server's scopes", () => {
    expect(covers("read", "read")).toBe(true);
    expect(covers("read", "operate")).toBe(false);
    expect(covers("operate", "read")).toBe(true);
    expect(covers("operate", "operate")).toBe(true);
    expect(covers("operate", "admin")).toBe(false);
    expect(covers("admin", "admin")).toBe(true);
    expect(covers("admin", "read")).toBe(true);
  });

  it("lets an unknown or missing level cover nothing", () => {
    expect(covers("", "read")).toBe(false);
    expect(covers(undefined, "read")).toBe(false);
    expect(covers("owner", "read")).toBe(false);
  });
});

describe("projectAccess", () => {
  it("derives what a project level allows", () => {
    expect(projectAccess({ access: "read" })).toEqual({ access: "read", operate: false, admin: false });
    expect(projectAccess({ access: "operate" })).toEqual({ access: "operate", operate: true, admin: false });
    expect(projectAccess({ access: "admin" })).toEqual({ access: "admin", operate: true, admin: true });
  });

  it("treats a project without access as an admin's, as before roles", () => {
    expect(projectAccess({})).toEqual({ access: "admin", operate: true, admin: true });
  });
});
