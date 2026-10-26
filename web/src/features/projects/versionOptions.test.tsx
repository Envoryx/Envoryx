import { render, screen } from "@testing-library/react";
import i18n from "@/i18n";
import type { RuntimeVersion } from "@/api/types";
import { versionBelow, versionOptions } from "./versionOptions";

const versions: RuntimeVersion[] = [
  { version: "8.6", image: "php:8.6", label: "PHP 8.6", preview: true, notInPlan: true },
  { version: "8.5", image: "php:8.5", label: "PHP 8.5", default: true },
  { version: "8.1", image: "php:8.1", label: "PHP 8.1", eol: true, notInPlan: true },
];

describe("versionOptions", () => {
  it("disables the versions the plan leaves out, except the one the service runs", () => {
    render(<select aria-label="Version">{versionOptions(versions, i18n.t, { keep: "8.1" })}</select>);
    const option = (name: string) => screen.getByRole("option", { name: new RegExp(`^${name}`) });
    expect(option("PHP 8.6")).toBeDisabled();
    expect(option("PHP 8.6")).toHaveTextContent("PHP 8.6 (preview) (not in your plan)");
    expect(option("PHP 8.5")).toBeEnabled();
    expect(option("PHP 8.1")).toBeEnabled();
  });
});

describe("versionOptions with a template's minimum", () => {
  const node: RuntimeVersion[] = [
    { version: "24", image: "node:24", label: "Node 24 LTS", default: true },
    { version: "20", image: "node:20", label: "Node 20 LTS", eol: true },
  ];

  it("disables the versions below it and says why", () => {
    render(<select aria-label="Version">{versionOptions(node, i18n.t, { min: "22" })}</select>);
    const option = (name: string) => screen.getByRole("option", { name: new RegExp(`^${name}`) });
    expect(option("Node 24")).toBeEnabled();
    expect(option("Node 20")).toBeDisabled();
    expect(option("Node 20")).toHaveTextContent("Node 20 LTS (end of life) (too old for this template)");
  });

  it("compares dotted versions numerically", () => {
    expect(versionBelow("20", "22")).toBe(true);
    expect(versionBelow("22", "22")).toBe(false);
    expect(versionBelow("100", "22")).toBe(false);
    expect(versionBelow("3.9", "3.10")).toBe(true);
    expect(versionBelow("18", undefined)).toBe(false);
  });
});
