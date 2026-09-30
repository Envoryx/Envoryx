import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { PhpConfigForm } from "./PhpConfigForm";
import type { PHPConfig, PHPExtension, RuntimeVersion } from "@/api/types";

const extensions: PHPExtension[] = [
  { name: "mbstring", description: "Multibyte strings", builtIn: true, available: true },
  { name: "gd", description: "Image processing", builtIn: false, available: true },
  { name: "imagick", description: "ImageMagick", builtIn: false, available: true },
  { name: "redis", description: "Redis client (phpredis)", builtIn: false, available: true },
];

const base: PHPConfig = {
  memoryLimit: "256M",
  uploadMaxFilesize: "64M",
  postMaxSize: "64M",
  maxExecutionTime: 120,
  displayErrors: true,
  errorReporting: "E_ALL",
  extensions: ["gd"],
};

const preview: RuntimeVersion = { version: "8.6", image: "ghcr.io/envoryx/envoryx-php:8.6", label: "PHP 8.6", preview: true, missingExtensions: ["imagick", "redis", "xdebug"] };

function Harness({ initial, version, onChange }: { initial: PHPConfig; version?: RuntimeVersion; onChange?: (c: PHPConfig) => void }) {
  const [value, setValue] = useState(initial);
  return (
    <PhpConfigForm
      value={value}
      onChange={(c) => {
        setValue(c);
        onChange?.(c);
      }}
      extensions={extensions}
      version={version}
    />
  );
}

const box = (name: string) => screen.getByText(name, { selector: "span.font-mono" }).closest("label")!.querySelector("input")!;

describe("PhpConfigForm", () => {
  it("greys out what the selected version's image lacks, Xdebug included", () => {
    render(<Harness initial={base} version={preview} />);
    expect(box("imagick")).toBeDisabled();
    expect(box("redis")).toBeDisabled();
    expect(box("gd")).toBeEnabled();
    expect(screen.getByLabelText(/Xdebug/)).toBeDisabled();
    expect(screen.getAllByText("Not available for PHP 8.6 yet")).toHaveLength(3);
  });

  it("keeps what is still on switchable so it can be switched off", async () => {
    let last: PHPConfig | undefined;
    render(<Harness initial={{ ...base, extensions: ["gd", "imagick"], xdebug: true }} version={preview} onChange={(c) => (last = c)} />);
    expect(screen.getAllByText("Not available for PHP 8.6 yet - switch it off to save")).toHaveLength(2);
    const user = userEvent.setup();
    await user.click(box("imagick"));
    expect(last?.extensions).toEqual(["gd"]);
    expect(box("imagick")).toBeDisabled();
    await user.click(screen.getByLabelText(/Xdebug/));
    expect(last?.xdebug).toBe(false);
    expect(screen.getByLabelText(/Xdebug/)).toBeDisabled();
  });

  it("offers everything on a version that has it", () => {
    render(<Harness initial={base} version={{ version: "8.5", image: "ghcr.io/envoryx/envoryx-php:8.5", label: "PHP 8.5" }} />);
    expect(box("imagick")).toBeEnabled();
    expect(screen.getByLabelText(/Xdebug/)).toBeEnabled();
    expect(screen.queryByText(/Not available for PHP/)).not.toBeInTheDocument();
  });
});
