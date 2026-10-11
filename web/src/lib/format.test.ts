import i18n from "@/i18n";
import { displayVersion, formatBytes, formatVersion } from "./format";

describe("displayVersion", () => {
  it.each([
    ["v0.10.0", "0.10.0"],
    ["v0.10.0-3-g73304d6", "0.10.0+3 (73304d6)"],
    ["v0.10.0-0-gcecec1a", "0.10.0 (cecec1a)"],
    ["v0.10.0-12-g73304d6-dirty", "0.10.0+12 (73304d6*)"],
    ["main-08025a835df43a81083de40f49f3539ff69ce0d6", "main (08025a8)"],
    ["dev", "dev"],
    ["ci-abc", "ci-abc"],
  ])("%s → %s", (raw, want) => expect(displayVersion(raw)).toBe(want));

  it("keeps a development build a development build", () => {
    const t = i18n.t.bind(i18n);
    expect(formatVersion(t, "v0.10.0-3-g73304d6", { current: "v0.10.0-3-g73304d6", enabled: true, release: false, available: false })).toBe("0.10.0+3 (73304d6) · development build");
    expect(formatVersion(t, "v0.9.0", { current: "v0.9.0", enabled: true, release: true, available: true, latest: "v0.10.0" })).toBe("0.9.0 · 0.10.0 available");
  });
});

describe("formatBytes", () => {
  it("names powers of 1024 as such", () => {
    expect(formatBytes(1_023_580_454_912)).toBe("953 GiB");
    expect(formatBytes(5 * 1024 * 1024)).toBe("5.0 MiB");
    expect(formatBytes(512)).toBe("512 B");
  });
});
