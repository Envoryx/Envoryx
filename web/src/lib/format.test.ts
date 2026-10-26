import i18n from "@/i18n";
import { displayVersion, formatBytes, formatVersion, slugify } from "./format";

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

describe("slugify", () => {
  // The same cases as TestSlugify in internal/validate, so the preview matches the server.
  it.each([
    ["Acme Shop", "acme-shop"],
    ["  My_Project.v2", "my-project-v2"],
    ["Ünïcode Name", "uenicode-name"],
    ["---", ""],
    ["legacy/shop", "legacy-shop"],
    ["a&b <script>", "ab-script"],
    ["Müller Shop", "mueller-shop"],
    ["Straße & Café", "strasse-cafe"],
    ["Łódź Zażółć", "lodz-zazolc"],
    ["Проект Ромашка", "proekt-romashka"],
    ["shop-php Тест", "shop-php-test"],
    ["Щука, ёж и Їжак", "shchuka-ezh-i-yizhak"],
    ["node-api відновлений", "node-api-vidnovleniy"],
    ["Подъезд 2", "podezd-2"],
    ["日本語", ""],
  ])("%s → %s", (name, want) => expect(slugify(name)).toBe(want));

  it("cuts at 40 characters without a trailing hyphen", () => {
    expect(slugify("a".repeat(39) + " b")).toBe("a".repeat(39));
  });
});

describe("formatBytes", () => {
  it("names powers of 1024 as such", () => {
    expect(formatBytes(1_023_580_454_912)).toBe("953 GiB");
    expect(formatBytes(5 * 1024 * 1024)).toBe("5.0 MiB");
    expect(formatBytes(512)).toBe("512 B");
  });
});
