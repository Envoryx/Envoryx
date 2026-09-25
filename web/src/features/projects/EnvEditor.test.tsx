import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { EnvEditor } from "./EnvEditor";
import type { EnvVar } from "@/api/types";
import { authedRoutes, mockApi, renderApp } from "@/test/utils";

const dotenv = [
  "APP_NAME=Shop",
  "APP_ENV=production",
  "APP_KEY=base64:abcdefghijklmnop",
  "DB_HOST=127.0.0.1",
  "DB_PASSWORD=old-secret",
  "REDIS_HOST=127.0.0.1",
  "lower_case=x",
  "MARIADB_ROOT_PASSWORD=x",
].join("\n");

describe("EnvEditor .env import and export", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("imports new and changed variables, leaves out what Envoryx sets and marks secrets", async () => {
    mockApi({ ...authedRoutes });
    const onChange = vi.fn();
    const current: EnvVar[] = [{ key: "APP_ENV", value: "local", isSecret: false }];
    renderApp(<EnvEditor value={current} onChange={onChange} services={[{ kind: "database", variant: "mariadb" }]} exportName="shop" />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Import .env" }));
    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getByLabelText(".env contents"));
    await user.paste(dotenv);

    const box = (name: string) => within(dialog).getByRole("checkbox", { name: `Import ${name}` });
    expect(box("APP_NAME")).toBeChecked();
    expect(box("APP_ENV")).toBeChecked();
    expect(within(dialog).getByText("replaces the current value")).toBeInTheDocument();
    // The project has a database: its variables stay Envoryx's unless picked.
    expect(box("DB_HOST")).not.toBeChecked();
    expect(box("DB_PASSWORD")).not.toBeChecked();
    expect(within(dialog).getAllByText("set by Envoryx (MariaDB)").length).toBe(2);
    // No Redis in this project: its variables are just variables.
    expect(box("REDIS_HOST")).toBeChecked();
    expect(box("lower_case")).toBeDisabled();
    expect(box("MARIADB_ROOT_PASSWORD")).toBeDisabled();
    // Secrets are recognised and masked.
    expect(within(dialog).queryByText("base64:abcdefghijklmnop")).not.toBeInTheDocument();

    await user.click(within(dialog).getByRole("button", { name: "Import 4 variables" }));
    expect(onChange).toHaveBeenCalledWith([
      { key: "APP_ENV", value: "production", isSecret: false },
      { key: "APP_NAME", value: "Shop", isSecret: false },
      { key: "APP_KEY", value: "base64:abcdefghijklmnop", isSecret: true },
      { key: "REDIS_HOST", value: "127.0.0.1", isSecret: false },
    ]);
  });

  it("exports the variables as a .env file", async () => {
    mockApi({ ...authedRoutes });
    let blob: Blob | undefined;
    vi.stubGlobal("URL", { ...URL, createObjectURL: (b: Blob) => ((blob = b), "blob:x"), revokeObjectURL: () => {} });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    renderApp(
      <EnvEditor
        value={[
          { key: "APP_ENV", value: "local", isSecret: false },
          { key: "APP_KEY", value: "two words", isSecret: true },
        ]}
        onChange={() => {}}
        exportName="shop"
      />,
    );
    await userEvent.setup().click(screen.getByRole("button", { name: "Export .env" }));
    expect(click).toHaveBeenCalled();
    const text = await blob!.text();
    expect(text).toContain("APP_ENV=local\n");
    expect(text).toContain('APP_KEY="two words"\n');
    expect(text.startsWith("# Environment of shop")).toBe(true);
    click.mockRestore();
  });
});
