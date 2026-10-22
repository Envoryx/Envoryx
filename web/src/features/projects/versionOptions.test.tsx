import { render, screen } from "@testing-library/react";
import i18n from "@/i18n";
import type { RuntimeVersion } from "@/api/types";
import { versionOptions } from "./versionOptions";

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
